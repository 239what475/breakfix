package taxonomy

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

type WorkStage string

const (
	WorkStageMapper WorkStage = "mapper"
	WorkStageReview WorkStage = "review"

	RuntimePurposeMapper = "taxonomy-mapper"
	RuntimePurposeReview = "taxonomy-review"
)

type MappingState string

const (
	MappingPending      MappingState = "Pending"
	MappingReadyPublish MappingState = "ReadyToPublish"
	MappingPublished    MappingState = "Published"
	MappingFailed       MappingState = "Failed"
	MappingCancelled    MappingState = "Cancelled"
)

type ReviewDecision string

const (
	ReviewApprove ReviewDecision = "approve"
	ReviewReject  ReviewDecision = "reject"
)

type Review struct {
	Decision ReviewDecision `json:"decision"`
	Feedback string         `json:"feedback,omitempty"`
}

type TaxonomyMapping struct {
	ID                string
	ChallengeID       string
	ChallengeRevision string
	BaseRevision      string
	ActiveStage       WorkStage
	ActiveRunID       string
	Candidate         *ChangeSet
	CurriculumReview  *Review
	SREReview         *Review
	Round             int
	State             MappingState
	PublishedRevision string
	LastError         string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// RunInput is the immutable, non-secret identity of a taxonomy Agent Run.
// Challenge content, snapshot data, and review feedback stay Server-owned and
// are resolved again through the authenticated internal API on every attempt.
type RunInput struct {
	WorkID string    `json:"work_id"`
	Stage  WorkStage `json:"stage"`
	Round  int       `json:"round"`
}

func (i RunInput) Validate() error {
	if strings.TrimSpace(i.WorkID) == "" {
		return errors.New("taxonomy run input requires work_id")
	}
	if i.Stage != WorkStageMapper && i.Stage != WorkStageReview {
		return errors.New("taxonomy run input has an invalid stage")
	}
	if i.Round < 0 {
		return errors.New("taxonomy run input has a negative round")
	}
	return nil
}

func DecodeRunInput(raw json.RawMessage) (RunInput, error) {
	var input RunInput
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return RunInput{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return RunInput{}, errors.New("taxonomy run input has a second JSON document")
		}
		return RunInput{}, err
	}
	if err := input.Validate(); err != nil {
		return RunInput{}, err
	}
	return input, nil
}

func PurposeForStage(stage WorkStage) (string, error) {
	switch stage {
	case WorkStageMapper:
		return RuntimePurposeMapper, nil
	case WorkStageReview:
		return RuntimePurposeReview, nil
	default:
		return "", errors.New("unknown taxonomy work stage")
	}
}
