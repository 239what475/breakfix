// Package roadmap owns the immutable course graph used by the catalog.
// Author classification and roadmap maintenance publish complete revisions;
// readers only observe one fully validated revision at a time.
package roadmap

import (
	"fmt"
	"slices"
)

const (
	KindDomain    = "Domain"
	KindTopic     = "Topic"
	KindTag       = "Tag"
	KindChallenge = "Challenge"
)

type Relation string

const (
	RelationPrecedes Relation = "precedes"
	RelationRelated  Relation = "related"
)

// Ref carries a runtime ID beside the portable source reference and title.
// Keeping all three values makes relationships both machine-addressable and
// directly reviewable without a second mutable lookup table.
type Ref struct {
	ID        string `yaml:"id" json:"id"`
	SourceRef string `yaml:"source_ref" json:"source_ref"`
	Title     string `yaml:"title" json:"title"`
}

type Domain struct {
	ID         string `yaml:"id" json:"id"`
	SourceRef  string `yaml:"source_ref" json:"source_ref"`
	Title      string `yaml:"title" json:"title"`
	Definition string `yaml:"definition" json:"definition"`
	Scope      string `yaml:"scope" json:"scope"`
	NonGoals   string `yaml:"non_goals" json:"non_goals"`

	File string `yaml:"-" json:"-"`
}

type Topic struct {
	ID                string `yaml:"id" json:"id"`
	SourceRef         string `yaml:"source_ref" json:"source_ref"`
	Title             string `yaml:"title" json:"title"`
	Domain            Ref    `yaml:"domain" json:"domain"`
	Definition        string `yaml:"definition" json:"definition"`
	Scope             string `yaml:"scope" json:"scope"`
	NonGoals          string `yaml:"non_goals" json:"non_goals"`
	ChallengeGuidance string `yaml:"challenge_guidance" json:"challenge_guidance"`

	File string `yaml:"-" json:"-"`
}

type Tag struct {
	ID          string `yaml:"id" json:"id"`
	SourceRef   string `yaml:"source_ref" json:"source_ref"`
	Title       string `yaml:"title" json:"title"`
	Description string `yaml:"description" json:"description"`

	File string `yaml:"-" json:"-"`
}

type ChallengeRef struct {
	ID              string `yaml:"id" json:"id"`
	SourceRef       string `yaml:"source_ref" json:"source_ref"`
	Title           string `yaml:"title" json:"title"`
	ContentRevision string `yaml:"content_revision" json:"content_revision"`
}

type ChallengeBinding struct {
	Challenge ChallengeRef `yaml:"challenge" json:"challenge"`
	Topic     Ref          `yaml:"topic" json:"topic"`
	Tags      []Ref        `yaml:"tags" json:"tags"`

	File string `yaml:"-" json:"-"`
}

type Edge struct {
	Source   Ref      `yaml:"source" json:"source"`
	Target   Ref      `yaml:"target" json:"target"`
	Relation Relation `yaml:"relation" json:"relation"`
	Reason   string   `yaml:"reason" json:"reason"`
}

// Revision is the sole complete runtime course projection. A published
// challenge has exactly one Topic binding and zero or more Tag bindings.
type Revision struct {
	Revision          string             `json:"revision"`
	Domains           []Domain           `json:"domains"`
	Topics            []Topic            `json:"topics"`
	Tags              []Tag              `json:"tags"`
	ChallengeBindings []ChallengeBinding `json:"challenge_bindings"`
	TopicEdges        []Edge             `json:"topic_edges"`
	ChallengeEdges    []Edge             `json:"challenge_edges"`
}

func (r Revision) Clone() Revision {
	clone := r
	clone.Domains = append([]Domain(nil), r.Domains...)
	clone.Topics = append([]Topic(nil), r.Topics...)
	clone.Tags = append([]Tag(nil), r.Tags...)
	clone.ChallengeBindings = append([]ChallengeBinding(nil), r.ChallengeBindings...)
	clone.TopicEdges = append([]Edge(nil), r.TopicEdges...)
	clone.ChallengeEdges = append([]Edge(nil), r.ChallengeEdges...)
	for index := range clone.ChallengeBindings {
		clone.ChallengeBindings[index].Tags = append([]Ref(nil), clone.ChallengeBindings[index].Tags...)
	}
	return clone
}

func (r Revision) Sorted() Revision {
	result := r.Clone()
	slices.SortFunc(result.Domains, func(left, right Domain) int { return compare(left.SourceRef, right.SourceRef) })
	slices.SortFunc(result.Topics, func(left, right Topic) int { return compare(left.SourceRef, right.SourceRef) })
	slices.SortFunc(result.Tags, func(left, right Tag) int { return compare(left.SourceRef, right.SourceRef) })
	slices.SortFunc(result.ChallengeBindings, func(left, right ChallengeBinding) int {
		return compare(left.Challenge.SourceRef, right.Challenge.SourceRef)
	})
	slices.SortFunc(result.TopicEdges, compareEdges)
	slices.SortFunc(result.ChallengeEdges, compareEdges)
	for index := range result.ChallengeBindings {
		slices.SortFunc(result.ChallengeBindings[index].Tags, compareRefs)
	}
	return result
}

func compare(left, right string) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func compareRefs(left, right Ref) int {
	if result := compare(left.SourceRef, right.SourceRef); result != 0 {
		return result
	}
	return compare(left.ID, right.ID)
}

func compareEdges(left, right Edge) int {
	if result := compare(left.Source.SourceRef, right.Source.SourceRef); result != 0 {
		return result
	}
	if result := compare(left.Target.SourceRef, right.Target.SourceRef); result != 0 {
		return result
	}
	return compare(string(left.Relation), string(right.Relation))
}

// PortableRef is the human-maintained reference used in Catalog Release
// source. Runtime IDs are intentionally absent from portable content.
type PortableRef struct {
	SourceRef string `yaml:"source_ref" json:"source_ref"`
	Title     string `yaml:"title" json:"title"`
}

type PortableDomain struct {
	Kind       string `yaml:"kind" json:"kind"`
	SourceRef  string `yaml:"source_ref" json:"source_ref"`
	Title      string `yaml:"title" json:"title"`
	Definition string `yaml:"definition" json:"definition"`
	Scope      string `yaml:"scope" json:"scope"`
	NonGoals   string `yaml:"non_goals" json:"non_goals"`

	File string `yaml:"-" json:"-"`
}

type PortableTopic struct {
	Kind              string      `yaml:"kind" json:"kind"`
	SourceRef         string      `yaml:"source_ref" json:"source_ref"`
	Title             string      `yaml:"title" json:"title"`
	Domain            PortableRef `yaml:"domain" json:"domain"`
	Definition        string      `yaml:"definition" json:"definition"`
	Scope             string      `yaml:"scope" json:"scope"`
	NonGoals          string      `yaml:"non_goals" json:"non_goals"`
	ChallengeGuidance string      `yaml:"challenge_guidance" json:"challenge_guidance"`

	File string `yaml:"-" json:"-"`
}

type PortableTag struct {
	Kind        string `yaml:"kind" json:"kind"`
	SourceRef   string `yaml:"source_ref" json:"source_ref"`
	Title       string `yaml:"title" json:"title"`
	Description string `yaml:"description" json:"description"`

	File string `yaml:"-" json:"-"`
}

type PortableChallengeRef struct {
	Path            string `yaml:"path" json:"path"`
	SourceRef       string `yaml:"source_ref" json:"source_ref"`
	Title           string `yaml:"title" json:"title"`
	ContentRevision string `yaml:"content_revision" json:"content_revision"`
}

type PortableChallengeBinding struct {
	Kind      string               `yaml:"kind" json:"kind"`
	Challenge PortableChallengeRef `yaml:"challenge" json:"challenge"`
	Topic     PortableRef          `yaml:"topic" json:"topic"`
	Tags      []PortableRef        `yaml:"tags" json:"tags"`

	File string `yaml:"-" json:"-"`
}

type PortableEdge struct {
	Source   PortableRef `yaml:"source" json:"source"`
	Target   PortableRef `yaml:"target" json:"target"`
	Relation Relation    `yaml:"relation" json:"relation"`
	Reason   string      `yaml:"reason" json:"reason"`
}

type PortableRevision struct {
	Domains           []PortableDomain           `json:"domains"`
	Topics            []PortableTopic            `json:"topics"`
	Tags              []PortableTag              `json:"tags"`
	ChallengeBindings []PortableChallengeBinding `json:"challenge_bindings"`
	TopicEdges        []PortableEdge             `json:"topic_edges"`
	ChallengeEdges    []PortableEdge             `json:"challenge_edges"`
}

// CompilePortable materializes a portable revision into runtime identities.
// challenges is keyed by the portable challenge path and must only contain
// challenges that have already become platform artifacts.
func CompilePortable(portable PortableRevision, challenges map[string]ChallengeRef) (Revision, error) {
	if err := portable.Validate(); err != nil {
		return Revision{}, err
	}
	result := Revision{
		Domains:           make([]Domain, 0, len(portable.Domains)),
		Topics:            make([]Topic, 0, len(portable.Topics)),
		Tags:              make([]Tag, 0, len(portable.Tags)),
		ChallengeBindings: make([]ChallengeBinding, 0, len(portable.ChallengeBindings)),
		TopicEdges:        make([]Edge, 0, len(portable.TopicEdges)),
		ChallengeEdges:    make([]Edge, 0, len(portable.ChallengeEdges)),
	}
	domains := make(map[string]Ref, len(portable.Domains))
	for _, value := range portable.Domains {
		ref := Ref{ID: RuntimeID(KindDomain, value.SourceRef), SourceRef: value.SourceRef, Title: value.Title}
		domains[value.SourceRef] = ref
		result.Domains = append(result.Domains, Domain{
			ID: ref.ID, SourceRef: ref.SourceRef, Title: ref.Title, Definition: value.Definition,
			Scope: value.Scope, NonGoals: value.NonGoals, File: value.File,
		})
	}
	topics := make(map[string]Ref, len(portable.Topics))
	for _, value := range portable.Topics {
		domain, exists := domains[value.Domain.SourceRef]
		if !exists || domain.Title != value.Domain.Title {
			return Revision{}, fmt.Errorf("portable topic %q references an unknown domain", value.SourceRef)
		}
		ref := Ref{ID: RuntimeID(KindTopic, value.SourceRef), SourceRef: value.SourceRef, Title: value.Title}
		topics[value.SourceRef] = ref
		result.Topics = append(result.Topics, Topic{
			ID: ref.ID, SourceRef: ref.SourceRef, Title: ref.Title, Domain: domain, Definition: value.Definition,
			Scope: value.Scope, NonGoals: value.NonGoals, ChallengeGuidance: value.ChallengeGuidance, File: value.File,
		})
	}
	tags := make(map[string]Ref, len(portable.Tags))
	for _, value := range portable.Tags {
		ref := Ref{ID: RuntimeID(KindTag, value.SourceRef), SourceRef: value.SourceRef, Title: value.Title}
		tags[value.SourceRef] = ref
		result.Tags = append(result.Tags, Tag{
			ID: ref.ID, SourceRef: ref.SourceRef, Title: ref.Title, Description: value.Description, File: value.File,
		})
	}
	challengesBySourceRef := make(map[string]Ref, len(portable.ChallengeBindings))
	for _, value := range portable.ChallengeBindings {
		challenge, exists := challenges[value.Challenge.Path]
		if !exists || challenge.SourceRef != value.Challenge.SourceRef || challenge.Title != value.Challenge.Title ||
			challenge.ContentRevision != value.Challenge.ContentRevision {
			return Revision{}, fmt.Errorf("portable challenge binding %q does not match a materialized challenge", value.Challenge.Path)
		}
		topic, exists := topics[value.Topic.SourceRef]
		if !exists || topic.Title != value.Topic.Title {
			return Revision{}, fmt.Errorf("portable challenge %q references an unknown topic", value.Challenge.SourceRef)
		}
		binding := ChallengeBinding{Challenge: challenge, Topic: topic, Tags: make([]Ref, 0, len(value.Tags)), File: value.File}
		for _, tag := range value.Tags {
			resolved, exists := tags[tag.SourceRef]
			if !exists || resolved.Title != tag.Title {
				return Revision{}, fmt.Errorf("portable challenge %q references an unknown tag", value.Challenge.SourceRef)
			}
			binding.Tags = append(binding.Tags, resolved)
		}
		result.ChallengeBindings = append(result.ChallengeBindings, binding)
		challengesBySourceRef[challenge.SourceRef] = Ref{ID: challenge.ID, SourceRef: challenge.SourceRef, Title: challenge.Title}
	}
	compileEdges := func(values []PortableEdge, definitions map[string]Ref) ([]Edge, error) {
		result := make([]Edge, 0, len(values))
		for _, value := range values {
			source, sourceExists := definitions[value.Source.SourceRef]
			target, targetExists := definitions[value.Target.SourceRef]
			if !sourceExists || !targetExists || source.Title != value.Source.Title || target.Title != value.Target.Title {
				return nil, errorsUnknownPortableEdge(value)
			}
			result = append(result, Edge{Source: source, Target: target, Relation: value.Relation, Reason: value.Reason})
		}
		return result, nil
	}
	var err error
	if result.TopicEdges, err = compileEdges(portable.TopicEdges, topics); err != nil {
		return Revision{}, err
	}
	if result.ChallengeEdges, err = compileEdges(portable.ChallengeEdges, challengesBySourceRef); err != nil {
		return Revision{}, err
	}
	result = result.Sorted()
	if err := result.Validate(); err != nil {
		return Revision{}, err
	}
	return result, nil
}

func errorsUnknownPortableEdge(edge PortableEdge) error {
	return fmt.Errorf("portable edge %q -> %q references an unknown or renamed entity", edge.Source.SourceRef, edge.Target.SourceRef)
}
