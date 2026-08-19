package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/rest-sh/restish/cli"
	"github.com/spf13/viper"
)

func TestValidateMaxResults(t *testing.T) {
	cases := []struct {
		name    string
		command string
		args    []string
		wantErr bool
		wantIn  []string
	}{
		{"over spec cap", "list-tickets", []string{"list-tickets", "--max-results", "101"}, true, []string{"101", "100", "list-tickets", "spec"}},
		{"over verified cap", "list-dimensions", []string{"list-dimensions", "--max-results", "501"}, true, []string{"501", "500", "verified against the live API"}},
		{"equals form", "list-dimensions", []string{"list-dimensions", "--max-results=1000"}, true, []string{"1000", "500"}},
		{"at cap", "list-dimensions", []string{"list-dimensions", "--max-results", "500"}, false, nil},
		{"under cap", "list-tickets", []string{"list-tickets", "--max-results", "50"}, false, nil},
		{"flag absent", "list-dimensions", []string{"list-dimensions"}, false, nil},
		{"unknown command", "list-reports", []string{"list-reports", "--max-results", "9999"}, false, nil},
		{"non-numeric left to pflag", "list-dimensions", []string{"list-dimensions", "--max-results", "many"}, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMaxResults(tc.command, tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err == nil {
				return
			}
			preflight, ok := err.(invocationPreflightError)
			if !ok {
				t.Fatalf("error type = %T, want invocationPreflightError", err)
			}
			if preflight.ExitCode() != exitUsage {
				t.Errorf("exit code = %d, want %d", preflight.ExitCode(), exitUsage)
			}
			if preflight.StructuredError().Code != "USAGE_ERROR" {
				t.Errorf("code = %q, want USAGE_ERROR", preflight.StructuredError().Code)
			}
			for _, fragment := range tc.wantIn {
				if !strings.Contains(preflight.StructuredError().Message, fragment) {
					t.Errorf("message %q missing %q", preflight.StructuredError().Message, fragment)
				}
			}
		})
	}
}

func TestPreflightAPIInvocationRejectsOverCapMaxResults(t *testing.T) {
	api := cli.API{Operations: []cli.Operation{{Name: "list-dimensions"}}}
	configureInvocationPreflightTest(t, api, true, false, false)

	err := preflightAPIInvocation([]string{"dci", "dci", "list-dimensions", "--max-results", "501"})
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("error = %v, want over-cap rejection naming the cap", err)
	}

	if err := preflightAPIInvocation([]string{"dci", "dci", "list-dimensions", "--max-results", "500"}); err != nil {
		t.Fatalf("at-cap invocation rejected: %v", err)
	}
}

func TestCollectionPageToken(t *testing.T) {
	if got := collectionPageToken(map[string]interface{}{"pageToken": "abc"}); got != "abc" {
		t.Errorf("pageToken = %q", got)
	}
	if got := collectionPageToken(map[string]interface{}{"nextPageToken": "n"}); got != "n" {
		t.Errorf("nextPageToken = %q", got)
	}
	if got := collectionPageToken(map[string]interface{}{"pageToken": "  "}); got != "" {
		t.Errorf("blank token = %q, want empty", got)
	}
	if got := collectionPageToken([]interface{}{"not", "a", "map"}); got != "" {
		t.Errorf("non-map = %q, want empty", got)
	}
}

func capturePaginationStderr(t *testing.T) *bytes.Buffer {
	t.Helper()
	oldStderr := cli.Stderr
	var stderr bytes.Buffer
	cli.Stderr = &stderr
	t.Cleanup(func() { cli.Stderr = oldStderr })
	return &stderr
}

func TestNotePageTokenDropped(t *testing.T) {
	listBody := func(token string) map[string]interface{} {
		return map[string]interface{}{
			"rowCount":  float64(50),
			"pageToken": token,
			"dimensions": []interface{}{
				map[string]interface{}{"id": "zone", "label": "Zone"},
			},
		}
	}

	stderr := capturePaginationStderr(t)
	notePageTokenDropped(listBody("c2t1"))
	note := stderr.String()
	if !strings.Contains(note, "c2t1") || !strings.Contains(note, "--page-token") || !strings.Contains(note, "50") {
		t.Errorf("note = %q, want token, flag, and row count", note)
	}

	// No token: silent.
	stderr = capturePaginationStderr(t)
	notePageTokenDropped(map[string]interface{}{
		"rowCount":   float64(1),
		"dimensions": []interface{}{map[string]interface{}{"id": "zone"}},
	})
	if stderr.String() != "" {
		t.Errorf("tokenless body produced note %q", stderr.String())
	}

	// Token but not list-shaped (no collection array): silent.
	stderr = capturePaginationStderr(t)
	notePageTokenDropped(map[string]interface{}{"pageToken": "x", "id": "r1"})
	if stderr.String() != "" {
		t.Errorf("non-list body produced note %q", stderr.String())
	}
}

func TestTableAndCSVMarshalNoteDroppedPageToken(t *testing.T) {
	viper.Set("table-columns", "")
	t.Cleanup(func() { viper.Set("table-columns", nil) })

	body := map[string]interface{}{
		"rowCount":  float64(1),
		"pageToken": "resume-me",
		"items": []interface{}{
			map[string]interface{}{"id": "a", "name": "Alpha"},
		},
	}

	stderr := capturePaginationStderr(t)
	if _, err := (dciCSVContentType{}).Marshal(body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "resume-me") {
		t.Errorf("csv note = %q, want continuation token", stderr.String())
	}

	stderr = capturePaginationStderr(t)
	if _, err := (dciTableContentType{}).Marshal(body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "resume-me") {
		t.Errorf("table note = %q, want continuation token", stderr.String())
	}
}
