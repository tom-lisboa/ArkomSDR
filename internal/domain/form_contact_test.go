package domain

import (
	"encoding/json"
	"os"
	"testing"
)

func TestFormContactMatchesTemplateAsset(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/form-contact-template.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Types map[string]struct {
			Body string `json:"body"`
		} `json:"types"`
	}
	if err = json.Unmarshal(raw, &v); err != nil || v.Types["twilio/text"].Body != FormContactText {
		t.Fatal("template text drift", err)
	}
}
