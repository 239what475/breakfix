package runnable

import (
	"errors"
	"strings"
)

// BlankRuntimePlan is the controller-installed runtime definition for a blank
// environment. Blank environments exist for free-form hands-on practice and
// deliberately bind no runnable revision, source archive, or artifact: the
// plan alone decides what the provider provisions.
type BlankRuntimePlan struct {
	Profile   RuntimeProfile  `json:"profile"`
	Lifecycle LifecyclePolicy `json:"lifecycle"`
	// Image is the immutable management terminal image the blank terminal runs.
	Image string `json:"image"`
}

func (p BlankRuntimePlan) Validate() error {
	if err := p.Profile.Validate(); err != nil {
		return err
	}
	if err := p.Lifecycle.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(p.Image) == "" {
		return errors.New("blank runtime plan requires a management terminal image")
	}
	if p.Profile.BaseImage != p.Image {
		return errors.New("blank runtime profile must pin the management terminal image as its base image")
	}
	return nil
}

func (p BlankRuntimePlan) Digest() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	return digest(p)
}
