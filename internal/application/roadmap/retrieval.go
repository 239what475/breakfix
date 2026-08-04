// Package roadmap provides deterministic, read-only retrieval over one
// immutable Roadmap revision. It deliberately has no mutation operations.
package roadmap

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode"

	"github.com/breakfix/breakfix/internal/content/challenge"
	domain "github.com/breakfix/breakfix/internal/domain/roadmap"
)

const maxSearchResults = 20

type TopicSearch struct {
	Query    string `json:"query"`
	DomainID string `json:"domain_id,omitempty"`
	Limit    int    `json:"limit"`
}

func (q TopicSearch) Validate() error {
	if strings.TrimSpace(q.Query) == "" || q.Limit < 1 || q.Limit > maxSearchResults {
		return fmt.Errorf("topic search requires a query and limit from 1 to %d", maxSearchResults)
	}
	return nil
}

type TagSearch struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type ChallengeSearch struct {
	Query   string `json:"query"`
	TopicID string `json:"topic_id,omitempty"`
	Limit   int    `json:"limit"`
}

func (q ChallengeSearch) Validate() error {
	if strings.TrimSpace(q.Query) == "" || q.Limit < 1 || q.Limit > maxSearchResults {
		return fmt.Errorf("challenge search requires a query and limit from 1 to %d", maxSearchResults)
	}
	return nil
}

func (q TagSearch) Validate() error {
	if strings.TrimSpace(q.Query) == "" || q.Limit < 1 || q.Limit > maxSearchResults {
		return fmt.Errorf("tag search requires a query and limit from 1 to %d", maxSearchResults)
	}
	return nil
}

// TopicMatch is intentionally compact. A Classifying Agent must call
// ReadTopic when it needs the complete definition before selecting a topic.
type TopicMatch struct {
	ID          string     `json:"id"`
	SourceRef   string     `json:"source_ref"`
	Title       string     `json:"title"`
	Summary     string     `json:"summary"`
	Domain      domain.Ref `json:"domain"`
	MatchReason string     `json:"match_reason"`
}

// TagMatch is intentionally compact. A Classifying Agent must call ReadTag
// when it needs the complete definition before selecting a tag.
type TagMatch struct {
	ID          string `json:"id"`
	SourceRef   string `json:"source_ref"`
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	MatchReason string `json:"match_reason"`
}

// ChallengeMatch is intentionally compact. A Roadmap Planner must explicitly
// read a selected challenge before it uses its full teaching material as
// evidence for a relationship proposal.
type ChallengeMatch struct {
	ID          string     `json:"id"`
	SourceRef   string     `json:"source_ref"`
	Title       string     `json:"title"`
	Summary     string     `json:"summary"`
	Topic       domain.Ref `json:"topic"`
	MatchReason string     `json:"match_reason"`
}

type ChallengeCheckpoint struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Node        string `json:"node,omitempty"`
}

type ChallengeDetail struct {
	Challenge   domain.ChallengeRef   `json:"challenge"`
	Topic       domain.Ref            `json:"topic"`
	Tags        []domain.Ref          `json:"tags"`
	Runtime     string                `json:"runtime"`
	Difficulty  string                `json:"difficulty"`
	Description string                `json:"description"`
	Problem     string                `json:"problem"`
	Solution    string                `json:"solution"`
	Checkpoints []ChallengeCheckpoint `json:"checkpoints"`
}

// ChallengeContentReader is the one non-Roadmap dependency available to a
// Planner's read_challenge tool. It may only return a challenge that still
// matches the fixed immutable binding supplied by Retrieval.
type ChallengeContentReader interface {
	ReadRoadmapChallenge(context.Context, domain.ChallengeBinding) (ChallengeContent, error)
}

type ChallengeContent struct {
	Runtime     string
	Difficulty  string
	Description string
	Problem     string
	Solution    string
	Checkpoints []ChallengeCheckpoint
}

// Retrieval binds every query to one immutable RoadmapRevision. It is safe to
// construct per leased run because the revision is small in the initial
// catalog; persistence and revision selection remain Server responsibilities.
type Retrieval struct {
	revision            domain.Revision
	excludedTopicID     string
	excludedChallengeID string
	challengeReader     ChallengeContentReader
}

func NewRetrieval(revision domain.Revision) (*Retrieval, error) {
	if !domain.ValidRevision(revision.Revision) {
		return nil, errors.New("roadmap retrieval requires an immutable revision")
	}
	if err := revision.Validate(); err != nil {
		return nil, fmt.Errorf("validate roadmap retrieval revision: %w", err)
	}
	return &Retrieval{revision: revision.Clone()}, nil
}

// NewPlannerRetrieval creates the narrower read model used by one Roadmap
// task. The subject is deliberately excluded from same-kind search and read
// operations so the model cannot manufacture a self-edge through retrieval.
func NewPlannerRetrieval(revision domain.Revision, kind domain.TaskKind, subject domain.Subject, reader ChallengeContentReader) (*Retrieval, error) {
	if !kind.Valid() || !subject.Valid(kind) || reader == nil {
		return nil, errors.New("roadmap planner retrieval requires a valid subject and challenge reader")
	}
	value, err := NewRetrieval(revision)
	if err != nil {
		return nil, err
	}
	switch kind {
	case domain.TaskTopic:
		if _, err := value.ReadTopic(subject.Ref.ID); err != nil {
			return nil, fmt.Errorf("roadmap planner subject: %w", err)
		}
		value.excludedTopicID = subject.Ref.ID
	case domain.TaskChallenge:
		found := false
		for _, binding := range value.revision.ChallengeBindings {
			if binding.Challenge.ID == subject.Ref.ID && binding.Challenge.SourceRef == subject.Ref.SourceRef && binding.Challenge.Title == subject.Ref.Title && binding.Challenge.ContentRevision == subject.ContentRevision {
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("roadmap planner challenge subject is absent from its revision")
		}
		value.excludedChallengeID = subject.Ref.ID
	}
	value.challengeReader = reader
	return value, nil
}

func (r *Retrieval) Revision() string {
	if r == nil {
		return ""
	}
	return r.revision.Revision
}

func (r *Retrieval) SearchTopics(query TopicSearch) ([]TopicMatch, error) {
	if r == nil {
		return nil, errors.New("roadmap retrieval is not configured")
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	documents := make([]topicDocument, 0, len(r.revision.Topics))
	for _, topic := range r.revision.Topics {
		if topic.ID == r.excludedTopicID {
			continue
		}
		if query.DomainID != "" && topic.Domain.ID != query.DomainID {
			continue
		}
		documents = append(documents, topicDocument{topic: topic})
	}
	scores := scoreDocuments(strings.TrimSpace(query.Query), topicSearchDocuments(documents))
	result := make([]TopicMatch, 0, min(query.Limit, len(documents)))
	for _, document := range documents {
		score, found := scores[document.topic.ID]
		if !found || score.value <= 0 {
			continue
		}
		result = append(result, TopicMatch{
			ID: document.topic.ID, SourceRef: document.topic.SourceRef, Title: document.topic.Title,
			Summary: compactSummary(document.topic.Definition), Domain: document.topic.Domain, MatchReason: score.reason,
		})
	}
	slices.SortFunc(result, func(left, right TopicMatch) int {
		leftScore := scores[left.ID].value
		rightScore := scores[right.ID].value
		if leftScore > rightScore {
			return -1
		}
		if leftScore < rightScore {
			return 1
		}
		return strings.Compare(left.SourceRef, right.SourceRef)
	})
	if len(result) > query.Limit {
		result = result[:query.Limit]
	}
	return result, nil
}

func (r *Retrieval) ReadTopic(id string) (*domain.Topic, error) {
	if r == nil || strings.TrimSpace(id) == "" {
		return nil, errors.New("roadmap topic read requires an id")
	}
	for _, topic := range r.revision.Topics {
		if topic.ID == r.excludedTopicID {
			continue
		}
		if topic.ID == id {
			value := topic
			return &value, nil
		}
	}
	return nil, fmt.Errorf("roadmap topic %q was not found in revision %s", id, r.revision.Revision)
}

func (r *Retrieval) SearchChallenges(query ChallengeSearch) ([]ChallengeMatch, error) {
	if r == nil {
		return nil, errors.New("roadmap retrieval is not configured")
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	topics := make(map[string]domain.Ref, len(r.revision.Topics))
	for _, topic := range r.revision.Topics {
		topics[topic.ID] = domain.Ref{ID: topic.ID, SourceRef: topic.SourceRef, Title: topic.Title}
	}
	if query.TopicID != "" {
		if _, exists := topics[query.TopicID]; !exists {
			return nil, fmt.Errorf("roadmap topic %q was not found in revision %s", query.TopicID, r.revision.Revision)
		}
	}
	documents := make([]challengeDocument, 0, len(r.revision.ChallengeBindings))
	for _, binding := range r.revision.ChallengeBindings {
		if binding.Challenge.ID == r.excludedChallengeID || query.TopicID != "" && binding.Topic.ID != query.TopicID {
			continue
		}
		documents = append(documents, challengeDocument{binding: binding})
	}
	scores := scoreDocuments(strings.TrimSpace(query.Query), challengeSearchDocuments(documents))
	result := make([]ChallengeMatch, 0, min(query.Limit, len(documents)))
	for _, document := range documents {
		score, found := scores[document.binding.Challenge.ID]
		if !found || score.value <= 0 {
			continue
		}
		result = append(result, ChallengeMatch{
			ID: document.binding.Challenge.ID, SourceRef: document.binding.Challenge.SourceRef, Title: document.binding.Challenge.Title,
			Summary: "所属 Topic：" + document.binding.Topic.Title, Topic: document.binding.Topic, MatchReason: score.reason,
		})
	}
	slices.SortFunc(result, func(left, right ChallengeMatch) int {
		leftScore := scores[left.ID].value
		rightScore := scores[right.ID].value
		if leftScore > rightScore {
			return -1
		}
		if leftScore < rightScore {
			return 1
		}
		return strings.Compare(left.SourceRef, right.SourceRef)
	})
	if len(result) > query.Limit {
		result = result[:query.Limit]
	}
	return result, nil
}

func (r *Retrieval) ReadChallenge(ctx context.Context, id string) (*ChallengeDetail, error) {
	if r == nil || r.challengeReader == nil || strings.TrimSpace(id) == "" {
		return nil, errors.New("roadmap challenge read requires a configured reader and id")
	}
	if id == r.excludedChallengeID {
		return nil, fmt.Errorf("roadmap challenge %q was not found in revision %s", id, r.revision.Revision)
	}
	for _, binding := range r.revision.ChallengeBindings {
		if binding.Challenge.ID != id {
			continue
		}
		content, err := r.challengeReader.ReadRoadmapChallenge(ctx, binding)
		if err != nil {
			return nil, err
		}
		return &ChallengeDetail{
			Challenge: binding.Challenge, Topic: binding.Topic, Tags: append([]domain.Ref(nil), binding.Tags...),
			Runtime: content.Runtime, Difficulty: content.Difficulty, Description: content.Description,
			Problem: content.Problem, Solution: content.Solution, Checkpoints: append([]ChallengeCheckpoint(nil), content.Checkpoints...),
		}, nil
	}
	return nil, fmt.Errorf("roadmap challenge %q was not found in revision %s", id, r.revision.Revision)
}

// FilesystemChallengeReader reads only the content of the materialized
// challenge named by an immutable Roadmap binding. The binding check prevents
// a late file replacement from silently changing a Planner's evidence.
type FilesystemChallengeReader struct {
	Root string
}

func (r FilesystemChallengeReader) ReadRoadmapChallenge(_ context.Context, binding domain.ChallengeBinding) (ChallengeContent, error) {
	entry, err := challenge.Get(r.Root, binding.Challenge.ID)
	if err != nil {
		return ChallengeContent{}, fmt.Errorf("read roadmap challenge %q: %w", binding.Challenge.ID, err)
	}
	if entry.Title != binding.Challenge.Title || entry.ContentRevision != binding.Challenge.ContentRevision {
		return ChallengeContent{}, fmt.Errorf("roadmap challenge %q no longer matches its fixed revision", binding.Challenge.ID)
	}
	content, err := challenge.ReadContent(entry)
	if err != nil {
		return ChallengeContent{}, fmt.Errorf("read roadmap challenge content %q: %w", binding.Challenge.ID, err)
	}
	checkpoints := make([]ChallengeCheckpoint, 0, len(entry.Checkpoints))
	for _, checkpoint := range entry.Checkpoints {
		checkpoints = append(checkpoints, ChallengeCheckpoint{
			ID: checkpoint.ID, Title: checkpoint.Title, Description: checkpoint.Description, Node: checkpoint.Node,
		})
	}
	return ChallengeContent{
		Runtime: entry.Runtime, Difficulty: entry.Difficulty, Description: entry.Description,
		Problem: content.Problem, Solution: content.Solution, Checkpoints: checkpoints,
	}, nil
}

func (r *Retrieval) SearchTags(query TagSearch) ([]TagMatch, error) {
	if r == nil {
		return nil, errors.New("roadmap retrieval is not configured")
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	documents := make([]tagDocument, 0, len(r.revision.Tags))
	for _, tag := range r.revision.Tags {
		documents = append(documents, tagDocument{tag: tag})
	}
	scores := scoreDocuments(strings.TrimSpace(query.Query), tagSearchDocuments(documents))
	result := make([]TagMatch, 0, min(query.Limit, len(documents)))
	for _, document := range documents {
		score, found := scores[document.tag.ID]
		if !found || score.value <= 0 {
			continue
		}
		result = append(result, TagMatch{
			ID: document.tag.ID, SourceRef: document.tag.SourceRef, Title: document.tag.Title,
			Summary: compactSummary(document.tag.Description), MatchReason: score.reason,
		})
	}
	slices.SortFunc(result, func(left, right TagMatch) int {
		leftScore := scores[left.ID].value
		rightScore := scores[right.ID].value
		if leftScore > rightScore {
			return -1
		}
		if leftScore < rightScore {
			return 1
		}
		return strings.Compare(left.SourceRef, right.SourceRef)
	})
	if len(result) > query.Limit {
		result = result[:query.Limit]
	}
	return result, nil
}

func (r *Retrieval) ReadTag(id string) (*domain.Tag, error) {
	if r == nil || strings.TrimSpace(id) == "" {
		return nil, errors.New("roadmap tag read requires an id")
	}
	for _, tag := range r.revision.Tags {
		if tag.ID == id {
			value := tag
			return &value, nil
		}
	}
	return nil, fmt.Errorf("roadmap tag %q was not found in revision %s", id, r.revision.Revision)
}

type topicDocument struct{ topic domain.Topic }
type tagDocument struct{ tag domain.Tag }
type challengeDocument struct{ binding domain.ChallengeBinding }

type searchDocument struct {
	id        string
	sourceRef string
	title     string
	fields    []weightedField
}

type weightedField struct {
	name   string
	value  string
	weight float64
}

func topicSearchDocuments(values []topicDocument) []searchDocument {
	result := make([]searchDocument, 0, len(values))
	for _, value := range values {
		topic := value.topic
		result = append(result, searchDocument{
			id: topic.ID, sourceRef: topic.SourceRef, title: topic.Title,
			fields: []weightedField{
				{name: "title", value: topic.Title, weight: 5},
				{name: "definition", value: topic.Definition, weight: 3},
				{name: "scope", value: topic.Scope, weight: 2},
				{name: "challenge_guidance", value: topic.ChallengeGuidance, weight: 2},
			},
		})
	}
	return result
}

func tagSearchDocuments(values []tagDocument) []searchDocument {
	result := make([]searchDocument, 0, len(values))
	for _, value := range values {
		tag := value.tag
		result = append(result, searchDocument{
			id: tag.ID, sourceRef: tag.SourceRef, title: tag.Title,
			fields: []weightedField{
				{name: "title", value: tag.Title, weight: 5},
				{name: "description", value: tag.Description, weight: 3},
			},
		})
	}
	return result
}

func challengeSearchDocuments(values []challengeDocument) []searchDocument {
	result := make([]searchDocument, 0, len(values))
	for _, value := range values {
		binding := value.binding
		result = append(result, searchDocument{
			id: binding.Challenge.ID, sourceRef: binding.Challenge.SourceRef, title: binding.Challenge.Title,
			fields: []weightedField{
				{name: "title", value: binding.Challenge.Title, weight: 5},
				{name: "topic", value: binding.Topic.Title, weight: 2},
			},
		})
	}
	return result
}

type searchScore struct {
	value  float64
	reason string
}

// scoreDocuments implements a compact BM25F ranking over the fields above.
// Exact IDs and titles are deterministic first-class matches, then weighted
// field terms rank the remaining candidates. Chinese text uses character
// bigrams while command-like English fragments remain independent tokens.
func scoreDocuments(query string, documents []searchDocument) map[string]searchScore {
	result := make(map[string]searchScore, len(documents))
	queryTokens := uniqueTokens(tokenize(query))
	if len(queryTokens) == 0 || len(documents) == 0 {
		return result
	}
	tokenCounts := make([][]map[string]int, len(documents))
	fieldAverageLengths := make(map[string]float64)
	documentFrequency := make(map[string]int)
	for index, document := range documents {
		tokenCounts[index] = make([]map[string]int, len(document.fields))
		seen := make(map[string]struct{})
		for fieldIndex, field := range document.fields {
			counts := countTokens(tokenize(field.value))
			tokenCounts[index][fieldIndex] = counts
			fieldAverageLengths[field.name] += float64(tokenCount(counts))
			for token := range counts {
				seen[token] = struct{}{}
			}
		}
		for token := range seen {
			documentFrequency[token]++
		}
	}
	for field := range fieldAverageLengths {
		fieldAverageLengths[field] /= float64(len(documents))
	}
	const k1 = 1.2
	const b = 0.75
	normalizedQuery := normalizeSearchText(query)
	for index, document := range documents {
		score := 0.0
		reasons := make([]string, 0, 2)
		if normalizedQuery == normalizeSearchText(document.id) || normalizedQuery == normalizeSearchText(document.sourceRef) {
			score += 10000
			reasons = append(reasons, "标识精确匹配")
		}
		if normalizedQuery == normalizeSearchText(document.title) {
			score += 9000
			reasons = append(reasons, "标题精确匹配")
		}
		for _, token := range queryTokens {
			df := documentFrequency[token]
			if df == 0 {
				continue
			}
			idf := math.Log(1 + (float64(len(documents)-df)+0.5)/(float64(df)+0.5))
			weightedTF := 0.0
			matchedFields := make([]string, 0, len(document.fields))
			for fieldIndex, field := range document.fields {
				count := tokenCounts[index][fieldIndex][token]
				if count == 0 {
					continue
				}
				length := float64(tokenCount(tokenCounts[index][fieldIndex]))
				average := fieldAverageLengths[field.name]
				if average == 0 {
					average = 1
				}
				weightedTF += field.weight * float64(count) / (1 - b + b*length/average)
				matchedFields = append(matchedFields, field.name)
			}
			if weightedTF == 0 {
				continue
			}
			score += idf * (weightedTF * (k1 + 1) / (k1 + weightedTF))
			if len(matchedFields) > 0 && len(reasons) < 2 {
				reasons = append(reasons, "匹配 "+strings.Join(matchedFields, "、"))
			}
		}
		if score > 0 {
			result[document.id] = searchScore{value: score, reason: strings.Join(reasons, "；")}
		}
	}
	return result
}

func tokenize(value string) []string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return nil
	}
	result := make([]string, 0)
	var latin strings.Builder
	flushLatin := func() {
		if latin.Len() == 0 {
			return
		}
		result = append(result, latin.String())
		latin.Reset()
	}
	han := make([]rune, 0)
	flushHan := func() {
		for _, value := range han {
			result = append(result, string(value))
		}
		for index := 0; index+1 < len(han); index++ {
			result = append(result, string(han[index:index+2]))
		}
		han = han[:0]
	}
	for _, value := range value {
		switch {
		case unicode.Is(unicode.Han, value):
			flushLatin()
			han = append(han, value)
		case unicode.IsLetter(value) || unicode.IsNumber(value) || value == '_' || value == '-' || value == '.':
			flushHan()
			latin.WriteRune(value)
		default:
			flushLatin()
			flushHan()
		}
	}
	flushLatin()
	flushHan()
	return result
}

func countTokens(values []string) map[string]int {
	result := make(map[string]int, len(values))
	for _, value := range values {
		result[value]++
	}
	return result
}

func tokenCount(values map[string]int) int {
	result := 0
	for _, count := range values {
		result += count
	}
	return result
}

func uniqueTokens(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func normalizeSearchText(value string) string {
	return strings.Join(tokenize(value), " ")
}

func compactSummary(value string) string {
	value = strings.TrimSpace(value)
	const limit = 180
	if len([]rune(value)) <= limit {
		return value
	}
	return string([]rune(value)[:limit]) + "..."
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
