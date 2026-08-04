package roadmap

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

var sourceSegmentPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

var ErrNoCurrentRevision = errors.New("roadmap current revision does not exist")

func (r Revision) Validate() error {
	if r.Revision != "" && !ValidRevision(r.Revision) {
		return errors.New("roadmap revision must be a lowercase sha256 digest")
	}
	domains, err := validateDomains(r.Domains)
	if err != nil {
		return err
	}
	topics, err := validateTopics(r.Topics, domains)
	if err != nil {
		return err
	}
	tags, err := validateTags(r.Tags)
	if err != nil {
		return err
	}
	challenges, err := validateBindings(r.ChallengeBindings, topics, tags)
	if err != nil {
		return err
	}
	if err := validateEdges(r.TopicEdges, topics, "topic"); err != nil {
		return err
	}
	if err := validateEdges(r.ChallengeEdges, challenges, "challenge"); err != nil {
		return err
	}
	return nil
}

func (r PortableRevision) Validate() error {
	domains, err := validatePortableDomains(r.Domains)
	if err != nil {
		return err
	}
	topics, err := validatePortableTopics(r.Topics, domains)
	if err != nil {
		return err
	}
	tags, err := validatePortableTags(r.Tags)
	if err != nil {
		return err
	}
	challenges, err := validatePortableBindings(r.ChallengeBindings, topics, tags)
	if err != nil {
		return err
	}
	if err := validatePortableEdges(r.TopicEdges, topics, "topic"); err != nil {
		return err
	}
	if err := validatePortableEdges(r.ChallengeEdges, challenges, "challenge"); err != nil {
		return err
	}
	return nil
}

func validateDomains(values []Domain) (map[string]Ref, error) {
	result := make(map[string]Ref, len(values))
	for _, value := range values {
		if value.ID != RuntimeID(KindDomain, value.SourceRef) || !validDomainSourceRef(value.SourceRef) || !validText(value.Title) ||
			!validText(value.Definition) || !validText(value.Scope) || !validText(value.NonGoals) {
			return nil, fmt.Errorf("domain %q is invalid", value.SourceRef)
		}
		if _, exists := result[value.SourceRef]; exists {
			return nil, fmt.Errorf("duplicate domain source_ref %q", value.SourceRef)
		}
		result[value.SourceRef] = Ref{ID: value.ID, SourceRef: value.SourceRef, Title: value.Title}
	}
	return result, nil
}

func validateTopics(values []Topic, domains map[string]Ref) (map[string]Ref, error) {
	result := make(map[string]Ref, len(values))
	names := make(map[string]struct{}, len(values))
	for _, value := range values {
		domain, exists := domains[value.Domain.SourceRef]
		if value.ID != RuntimeID(KindTopic, value.SourceRef) || !exists || value.Domain != domain ||
			!validTopicSourceRef(value.SourceRef, domain.SourceRef) || !validText(value.Title) || !validText(value.Definition) ||
			!validText(value.Scope) || !validText(value.NonGoals) || !validText(value.ChallengeGuidance) {
			return nil, fmt.Errorf("topic %q is invalid", value.SourceRef)
		}
		if _, exists := result[value.SourceRef]; exists {
			return nil, fmt.Errorf("duplicate topic source_ref %q", value.SourceRef)
		}
		name := domain.SourceRef + "\x00" + normalizedTitle(value.Title)
		if _, exists := names[name]; exists {
			return nil, fmt.Errorf("duplicate normalized topic title %q in domain %q", value.Title, domain.SourceRef)
		}
		names[name] = struct{}{}
		result[value.SourceRef] = Ref{ID: value.ID, SourceRef: value.SourceRef, Title: value.Title}
	}
	return result, nil
}

func validateTags(values []Tag) (map[string]Ref, error) {
	result := make(map[string]Ref, len(values))
	names := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value.ID != RuntimeID(KindTag, value.SourceRef) || !validTagSourceRef(value.SourceRef) || !validText(value.Title) || !validText(value.Description) {
			return nil, fmt.Errorf("tag %q is invalid", value.SourceRef)
		}
		if _, exists := result[value.SourceRef]; exists {
			return nil, fmt.Errorf("duplicate tag source_ref %q", value.SourceRef)
		}
		name := normalizedTitle(value.Title)
		if _, exists := names[name]; exists {
			return nil, fmt.Errorf("duplicate normalized tag title %q", value.Title)
		}
		names[name] = struct{}{}
		result[value.SourceRef] = Ref{ID: value.ID, SourceRef: value.SourceRef, Title: value.Title}
	}
	return result, nil
}

func validateBindings(values []ChallengeBinding, topics, tags map[string]Ref) (map[string]Ref, error) {
	result := make(map[string]Ref, len(values))
	for _, value := range values {
		challenge := value.Challenge
		if !validRuntimeChallenge(challenge) || !validChallengeSourceRef(challenge.SourceRef, topics) {
			return nil, fmt.Errorf("challenge binding %q is invalid", challenge.SourceRef)
		}
		topic, exists := topics[value.Topic.SourceRef]
		if !exists || value.Topic != topic {
			return nil, fmt.Errorf("challenge %q references an unknown topic", challenge.SourceRef)
		}
		if _, exists := result[challenge.SourceRef]; exists {
			return nil, fmt.Errorf("duplicate challenge source_ref %q", challenge.SourceRef)
		}
		if err := validateTagRefs(value.Tags, tags); err != nil {
			return nil, fmt.Errorf("challenge %q tags: %w", challenge.SourceRef, err)
		}
		result[challenge.SourceRef] = Ref{ID: challenge.ID, SourceRef: challenge.SourceRef, Title: challenge.Title}
	}
	return result, nil
}

func validateEdges(values []Edge, definitions map[string]Ref, kind string) error {
	graph := make(map[string][]string, len(definitions))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !value.Relation.valid() || !validText(value.Reason) || value.Source.SourceRef == value.Target.SourceRef {
			return fmt.Errorf("%s edge is invalid", kind)
		}
		source, sourceExists := definitions[value.Source.SourceRef]
		target, targetExists := definitions[value.Target.SourceRef]
		if !sourceExists || !targetExists || value.Source != source || value.Target != target {
			return fmt.Errorf("%s edge %q -> %q references an unknown entity", kind, value.Source.SourceRef, value.Target.SourceRef)
		}
		if value.Relation == RelationRelated && value.Source.SourceRef > value.Target.SourceRef {
			return fmt.Errorf("%s related edge is not canonically ordered", kind)
		}
		key := value.Source.SourceRef + "\x00" + value.Target.SourceRef + "\x00" + string(value.Relation)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate %s edge %q -> %q", kind, value.Source.SourceRef, value.Target.SourceRef)
		}
		seen[key] = struct{}{}
		if value.Relation == RelationPrecedes {
			graph[value.Source.SourceRef] = append(graph[value.Source.SourceRef], value.Target.SourceRef)
		}
	}
	return validateAcyclic(graph, kind)
}

func validatePortableDomains(values []PortableDomain) (map[string]PortableRef, error) {
	result := make(map[string]PortableRef, len(values))
	for _, value := range values {
		if value.Kind != KindDomain || !validDomainSourceRef(value.SourceRef) || !validText(value.Title) || !validText(value.Definition) ||
			!validText(value.Scope) || !validText(value.NonGoals) {
			return nil, fmt.Errorf("portable domain %q is invalid", value.SourceRef)
		}
		if _, exists := result[value.SourceRef]; exists {
			return nil, fmt.Errorf("duplicate portable domain source_ref %q", value.SourceRef)
		}
		result[value.SourceRef] = PortableRef{SourceRef: value.SourceRef, Title: value.Title}
	}
	return result, nil
}

func validatePortableTopics(values []PortableTopic, domains map[string]PortableRef) (map[string]PortableRef, error) {
	result := make(map[string]PortableRef, len(values))
	names := make(map[string]struct{}, len(values))
	for _, value := range values {
		domain, exists := domains[value.Domain.SourceRef]
		if value.Kind != KindTopic || !exists || value.Domain != domain || !validTopicSourceRef(value.SourceRef, domain.SourceRef) ||
			!validText(value.Title) || !validText(value.Definition) || !validText(value.Scope) || !validText(value.NonGoals) || !validText(value.ChallengeGuidance) {
			return nil, fmt.Errorf("portable topic %q is invalid", value.SourceRef)
		}
		if _, exists := result[value.SourceRef]; exists {
			return nil, fmt.Errorf("duplicate portable topic source_ref %q", value.SourceRef)
		}
		name := domain.SourceRef + "\x00" + normalizedTitle(value.Title)
		if _, exists := names[name]; exists {
			return nil, fmt.Errorf("duplicate normalized portable topic title %q in domain %q", value.Title, domain.SourceRef)
		}
		names[name] = struct{}{}
		result[value.SourceRef] = PortableRef{SourceRef: value.SourceRef, Title: value.Title}
	}
	return result, nil
}

func validatePortableTags(values []PortableTag) (map[string]PortableRef, error) {
	result := make(map[string]PortableRef, len(values))
	names := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value.Kind != KindTag || !validTagSourceRef(value.SourceRef) || !validText(value.Title) || !validText(value.Description) {
			return nil, fmt.Errorf("portable tag %q is invalid", value.SourceRef)
		}
		if _, exists := result[value.SourceRef]; exists {
			return nil, fmt.Errorf("duplicate portable tag source_ref %q", value.SourceRef)
		}
		name := normalizedTitle(value.Title)
		if _, exists := names[name]; exists {
			return nil, fmt.Errorf("duplicate normalized portable tag title %q", value.Title)
		}
		names[name] = struct{}{}
		result[value.SourceRef] = PortableRef{SourceRef: value.SourceRef, Title: value.Title}
	}
	return result, nil
}

func validatePortableBindings(values []PortableChallengeBinding, topics, tags map[string]PortableRef) (map[string]PortableRef, error) {
	result := make(map[string]PortableRef, len(values))
	paths := make(map[string]struct{}, len(values))
	for _, value := range values {
		challenge := value.Challenge
		if value.Kind != KindChallenge || !validPortableChallenge(challenge) || !validChallengePortableSourceRef(challenge.SourceRef, topics) {
			return nil, fmt.Errorf("portable challenge binding %q is invalid", challenge.SourceRef)
		}
		topic, exists := topics[value.Topic.SourceRef]
		if !exists || value.Topic != topic {
			return nil, fmt.Errorf("portable challenge %q references an unknown topic", challenge.SourceRef)
		}
		if _, exists := result[challenge.SourceRef]; exists {
			return nil, fmt.Errorf("duplicate portable challenge source_ref %q", challenge.SourceRef)
		}
		if _, exists := paths[challenge.Path]; exists {
			return nil, fmt.Errorf("duplicate portable challenge path %q", challenge.Path)
		}
		if err := validatePortableTagRefs(value.Tags, tags); err != nil {
			return nil, fmt.Errorf("portable challenge %q tags: %w", challenge.SourceRef, err)
		}
		paths[challenge.Path] = struct{}{}
		result[challenge.SourceRef] = PortableRef{SourceRef: challenge.SourceRef, Title: challenge.Title}
	}
	return result, nil
}

func validatePortableEdges(values []PortableEdge, definitions map[string]PortableRef, kind string) error {
	graph := make(map[string][]string, len(definitions))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !value.Relation.valid() || !validText(value.Reason) || value.Source.SourceRef == value.Target.SourceRef {
			return fmt.Errorf("portable %s edge is invalid", kind)
		}
		source, sourceExists := definitions[value.Source.SourceRef]
		target, targetExists := definitions[value.Target.SourceRef]
		if !sourceExists || !targetExists || value.Source != source || value.Target != target {
			return fmt.Errorf("portable %s edge %q -> %q references an unknown entity", kind, value.Source.SourceRef, value.Target.SourceRef)
		}
		if value.Relation == RelationRelated && value.Source.SourceRef > value.Target.SourceRef {
			return fmt.Errorf("portable %s related edge is not canonically ordered", kind)
		}
		key := value.Source.SourceRef + "\x00" + value.Target.SourceRef + "\x00" + string(value.Relation)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate portable %s edge %q -> %q", kind, value.Source.SourceRef, value.Target.SourceRef)
		}
		seen[key] = struct{}{}
		if value.Relation == RelationPrecedes {
			graph[value.Source.SourceRef] = append(graph[value.Source.SourceRef], value.Target.SourceRef)
		}
	}
	return validateAcyclic(graph, "portable "+kind)
}

func validateTagRefs(values []Ref, tags map[string]Ref) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		tag, exists := tags[value.SourceRef]
		if !exists || value != tag {
			return fmt.Errorf("references unknown tag %q", value.SourceRef)
		}
		if _, exists := seen[value.SourceRef]; exists {
			return fmt.Errorf("duplicates tag %q", value.SourceRef)
		}
		seen[value.SourceRef] = struct{}{}
	}
	return nil
}

func validatePortableTagRefs(values []PortableRef, tags map[string]PortableRef) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		tag, exists := tags[value.SourceRef]
		if !exists || value != tag {
			return fmt.Errorf("references unknown tag %q", value.SourceRef)
		}
		if _, exists := seen[value.SourceRef]; exists {
			return fmt.Errorf("duplicates tag %q", value.SourceRef)
		}
		seen[value.SourceRef] = struct{}{}
	}
	return nil
}

func validateAcyclic(graph map[string][]string, kind string) error {
	state := make(map[string]uint8, len(graph))
	var visit func(string) error
	visit = func(value string) error {
		switch state[value] {
		case 1:
			return fmt.Errorf("%s precedes graph contains a cycle at %q", kind, value)
		case 2:
			return nil
		}
		state[value] = 1
		children := append([]string(nil), graph[value]...)
		slices.Sort(children)
		for _, child := range children {
			if err := visit(child); err != nil {
				return err
			}
		}
		state[value] = 2
		return nil
	}
	keys := make([]string, 0, len(graph))
	for key := range graph {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if err := visit(key); err != nil {
			return err
		}
	}
	return nil
}

func (r Relation) valid() bool { return r == RelationPrecedes || r == RelationRelated }

func validDomainSourceRef(value string) bool {
	return sourceSegmentPattern.MatchString(value)
}

func validTopicSourceRef(value, domain string) bool {
	prefix := domain + "/"
	return strings.HasPrefix(value, prefix) && sourceSegmentPattern.MatchString(strings.TrimPrefix(value, prefix))
}

func validTagSourceRef(value string) bool {
	return sourceSegmentPattern.MatchString(value)
}

func validChallengeSourceRef(value string, topics map[string]Ref) bool {
	for sourceRef := range topics {
		if validChallengeSourceRefForTopic(value, sourceRef) {
			return true
		}
	}
	return false
}

func validChallengePortableSourceRef(value string, topics map[string]PortableRef) bool {
	for sourceRef := range topics {
		if validChallengeSourceRefForTopic(value, sourceRef) {
			return true
		}
	}
	return false
}

func validChallengeSourceRefForTopic(value, topic string) bool {
	prefix := topic + "/"
	return strings.HasPrefix(value, prefix) && sourceSegmentPattern.MatchString(strings.TrimPrefix(value, prefix))
}

func validRuntimeChallenge(value ChallengeRef) bool {
	return strings.TrimSpace(value.ID) != "" && validText(value.Title) && ValidRevision(value.ContentRevision)
}

func validPortableChallenge(value PortableChallengeRef) bool {
	if !validText(value.Title) || !ValidRevision(value.ContentRevision) || value.Path == "" || strings.TrimSpace(value.Path) != value.Path ||
		strings.Contains(value.Path, "\\") || path.IsAbs(value.Path) {
		return false
	}
	clean := path.Clean(value.Path)
	return clean == value.Path && strings.HasPrefix(clean, "challenges/") && strings.TrimPrefix(clean, "challenges/") != ""
}

func validText(value string) bool {
	return strings.TrimSpace(value) != ""
}

func normalizedTitle(value string) string {
	return strings.ToLower(strings.Join(strings.FieldsFunc(strings.TrimSpace(value), unicode.IsSpace), " "))
}
