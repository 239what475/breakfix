package taxonomy

import "time"

type WorkKind string

const WorkKindMapping WorkKind = "mapping"

type WorkAgent string

const (
	WorkAgentMapper     WorkAgent = "mapper"
	WorkAgentCurriculum WorkAgent = "curriculum-reviewer"
	WorkAgentSRE        WorkAgent = "sre-reviewer"
)

type WorkState string

const (
	WorkPending      WorkState = "Pending"
	WorkReadyPublish WorkState = "ReadyToPublish"
	WorkPublished    WorkState = "Published"
	WorkFailed       WorkState = "Failed"
	WorkCancelled    WorkState = "Cancelled"
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

type WorkItem struct {
	ID                string
	Kind              WorkKind
	ChallengeID       string
	ChallengeRevision string
	BaseRevision      string
	MapperSessionID   string
	MapperStarted     bool
	CurriculumSession string
	CurriculumStarted bool
	SRESession        string
	SREStarted        bool
	Candidate         *ChangeSet
	CurriculumReview  *Review
	SREReview         *Review
	Round             int
	TechnicalFailures int
	ExecutionFailures int
	NextRunAt         time.Time
	State             WorkState
	PublishedRevision string
	LastError         string
	LeaseOwner        string
	LeaseExpiresAt    time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}
