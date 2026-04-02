package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type GitHubClient struct {
	token string
	owner string
	repo  string
	base  string
}

func NewGitHubClient(token, owner, repo string) *GitHubClient {
	return &GitHubClient{
		token: token,
		owner: owner,
		repo:  repo,
		base:  fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, repo),
	}
}

// ── Branch ───────────────────────────────────────────────────

func (c *GitHubClient) CreateBranch(name string) error {
	var ref struct {
		Object struct{ SHA string } `json:"object"`
	}
	if err := c.get("/git/refs/heads/main", &ref); err != nil {
		// Пробуем master
		if err2 := c.get("/git/refs/heads/master", &ref); err2 != nil {
			return fmt.Errorf("get default branch SHA: %w", err)
		}
	}
	return c.post("/git/refs", map[string]string{
		"ref": "refs/heads/" + name,
		"sha": ref.Object.SHA,
	}, nil)
}

// ── Files ────────────────────────────────────────────────────

func (c *GitHubClient) GetContent(path, branch string) (string, error) {
	var result struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		Message  string `json:"message"` // ошибка 404
	}
	if err := c.get(fmt.Sprintf("/contents/%s?ref=%s", path, branch), &result); err != nil {
		return "", err
	}
	if result.Message != "" {
		return "", fmt.Errorf("not found: %s", path)
	}
	if result.Encoding != "base64" {
		return result.Content, nil
	}
	raw := strings.ReplaceAll(result.Content, "\n", "")
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

func (c *GitHubClient) WriteFile(branch, path, content, message string) error {
	// SHA существующего файла (нужен для update)
	var existing struct {
		SHA string `json:"sha"`
	}
	c.get(fmt.Sprintf("/contents/%s?ref=%s", path, branch), &existing)

	body := map[string]any{
		"message": message,
		"content": base64.StdEncoding.EncodeToString([]byte(content)),
		"branch":  branch,
	}
	if existing.SHA != "" {
		body["sha"] = existing.SHA
	}
	return c.put(fmt.Sprintf("/contents/%s", path), body, nil)
}

func (c *GitHubClient) ListDir(dir, branch string) ([]string, error) {
	var items []struct {
		Path string `json:"path"`
		Type string `json:"type"`
	}
	url := fmt.Sprintf("/contents/%s?ref=%s", dir, branch)
	if err := c.get(url, &items); err != nil {
		return nil, err
	}
	var result []string
	for _, item := range items {
		if item.Type == "dir" {
			result = append(result, item.Path+"/")
		} else {
			result = append(result, item.Path)
		}
	}
	return result, nil
}

func (c *GitHubClient) SearchCode(query string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/search/code?q=%s+repo:%s/%s",
		query, c.owner, c.repo)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Items []struct {
			Path string `json:"path"`
		} `json:"items"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	var paths []string
	for _, item := range result.Items {
		paths = append(paths, item.Path)
	}
	return strings.Join(paths, "\n"), nil
}

// ── PRs ──────────────────────────────────────────────────────

type PR struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"html_url"`
}

func (c *GitHubClient) CreatePR(branch, title, body string) (int, string, error) {
	var pr PR
	err := c.post("/pulls", map[string]string{
		"title": title,
		"head":  branch,
		"base":  "main",
		"body":  body,
	}, &pr)
	if err != nil {
		// Пробуем master
		err = c.post("/pulls", map[string]string{
			"title": title,
			"head":  branch,
			"base":  "master",
			"body":  body,
		}, &pr)
	}
	return pr.Number, pr.URL, err
}

func (c *GitHubClient) ListPRs() ([]PR, error) {
	var prs []PR
	return prs, c.get("/pulls?state=open&per_page=10", &prs)
}

func (c *GitHubClient) MergePR(number int) error {
	return c.post(fmt.Sprintf("/pulls/%d/merge", number),
		map[string]string{"merge_method": "squash"}, nil)
}

func (c *GitHubClient) ClosePR(number int) error {
	return c.patch(fmt.Sprintf("/pulls/%d", number),
		map[string]string{"state": "closed"}, nil)
}

// ── CI Checks ────────────────────────────────────────────────

// WatchChecks — polling CI статуса, возвращает "success" | "failure" | "timeout"
func (c *GitHubClient) WatchChecks(prNumber int) string {
	var pr struct {
		Head struct{ SHA string } `json:"head"`
	}
	if err := c.get(fmt.Sprintf("/pulls/%d", prNumber), &pr); err != nil {
		return "failure"
	}

	deadline := time.Now().Add(20 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(30 * time.Second)
		status := c.commitStatus(pr.Head.SHA)
		if status == "success" || status == "failure" {
			return status
		}
	}
	return "timeout"
}

func (c *GitHubClient) commitStatus(sha string) string {
	// Сначала check runs (GitHub Actions)
	var checks struct {
		CheckRuns []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if err := c.get(fmt.Sprintf("/commits/%s/check-runs", sha), &checks); err == nil {
		if len(checks.CheckRuns) > 0 {
			allDone := true
			for _, cr := range checks.CheckRuns {
				if cr.Status != "completed" {
					allDone = false
				}
			}
			if allDone {
				for _, cr := range checks.CheckRuns {
					if cr.Conclusion == "failure" || cr.Conclusion == "cancelled" {
						return "failure"
					}
				}
				return "success"
			}
			return "pending"
		}
	}

	// Fallback: commit status
	var status struct {
		State string `json:"state"`
	}
	c.get(fmt.Sprintf("/commits/%s/status", sha), &status)
	switch status.State {
	case "success":
		return "success"
	case "failure", "error":
		return "failure"
	}
	return "pending"
}

func (c *GitHubClient) GetFailLog(prNumber int) string {
	var runs struct {
		WorkflowRuns []struct {
			ID         int    `json:"id"`
			Conclusion string `json:"conclusion"`
		} `json:"workflow_runs"`
	}
	c.get(fmt.Sprintf("/actions/runs?per_page=5"), &runs)

	for _, run := range runs.WorkflowRuns {
		if run.Conclusion == "failure" {
			var jobs struct {
				Jobs []struct {
					ID         int    `json:"id"`
					Conclusion string `json:"conclusion"`
				} `json:"jobs"`
			}
			c.get(fmt.Sprintf("/actions/runs/%d/jobs", run.ID), &jobs)
			for _, job := range jobs.Jobs {
				if job.Conclusion == "failure" {
					log, _ := c.jobLog(job.ID)
					return log
				}
			}
		}
	}
	return "no logs found"
}

func (c *GitHubClient) jobLog(jobID int) (string, error) {
	req, _ := http.NewRequest("GET",
		fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/jobs/%d/logs", c.owner, c.repo, jobID),
		nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	s := string(b)
	if len(s) > 3000 {
		s = s[len(s)-3000:]
	}
	return s, nil
}

// ── HTTP helpers ─────────────────────────────────────────────

func (c *GitHubClient) get(path string, out any) error {
	url := path
	if !strings.HasPrefix(path, "https://") {
		url = c.base + path
	}
	req, _ := http.NewRequest("GET", url, nil)
	return c.do(req, out)
}

func (c *GitHubClient) post(path string, body, out any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", c.base+path, bytes.NewReader(b))
	return c.do(req, out)
}

func (c *GitHubClient) put(path string, body, out any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("PUT", c.base+path, bytes.NewReader(b))
	return c.do(req, out)
}

func (c *GitHubClient) patch(path string, body, out any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("PATCH", c.base+path, bytes.NewReader(b))
	return c.do(req, out)
}

func (c *GitHubClient) do(req *http.Request, out any) error {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitHub %d: %s", resp.StatusCode, string(b))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
