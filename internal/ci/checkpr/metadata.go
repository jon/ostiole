package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

type pullRequestMetadata struct {
	Title string
	Body  string
}

var htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)

var requiredPullRequestSections = []string{
	"What this does",
	"Why",
	"Validation",
	"Documentation and evidence",
}

func readPullRequestEvent(name string) (pullRequestMetadata, error) {
	content, err := os.ReadFile(name)
	if err != nil {
		return pullRequestMetadata{}, err
	}
	var event struct {
		PullRequest struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(content, &event); err != nil {
		return pullRequestMetadata{}, fmt.Errorf("parse pull-request event: %w", err)
	}
	return pullRequestMetadata{
		Title: event.PullRequest.Title,
		Body:  event.PullRequest.Body,
	}, nil
}

func checkPullRequest(metadata pullRequestMetadata) []finding {
	findings := checkSubject("pull-request", strings.TrimSpace(metadata.Title))
	sections := pullRequestSections(htmlComment.ReplaceAllString(metadata.Body, ""))
	for _, required := range requiredPullRequestSections {
		if strings.TrimSpace(sections[required]) != "" {
			continue
		}
		findings = append(findings, newFinding(
			errorLevel, "pr-body", "pull-request",
			fmt.Sprintf("pull-request section %q is missing or empty", required),
		))
	}
	return findings
}

func pullRequestSections(body string) map[string]string {
	sections := make(map[string]string)
	current := ""
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "## ") {
			current = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			continue
		}
		if current != "" {
			sections[current] += line + "\n"
		}
	}
	return sections
}
