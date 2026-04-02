package github

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	token string
	owner string
	repo  string
	base  string
}

func NewClient(token, owner, repo string) *Client {
	return &Client{
		token: token,
		owner: owner,
		repo:  repo,
		base:  fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, repo),
	}
}

// --- Branch ---

func (c *Client) CreateBranch(name string) error {
	// Получаем SHA main
	var ref struct {
		Object struct{ SHA string } `json:"object"`
	}
	if err := c.get("/git/refs/heads/main", &ref); err != nil {
		return fmt.Errorf("get main SHA: %w", err)
	}

	body := map[string]string{
		"ref": "refs/heads/" + name,
		"sha": ref.Object.SHA,
	}
	return c.post("/git/refs", body, nil)
}

// --- Files ---

func (c *Client) GetFileContent(path, branch string) (string, error) {
	var result struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	url := fmt.Sprintf("/contents/%s?ref=%s", path, branch)
	if err := c.get(url, &result); err != nil {
		return "", err
	}
	if result.Encoding == "base64" {
		decoded, err := base64.StdEncoding.DecodeString(
			// GitHub добавляет \n в base64
			replaceAll(result.Content, "\n", ""),
		)
		if err != nil {
			return "", err
		}
		return string(decoded), nil
	}
	return result.Content, nil
}

func (c *Client) WriteFile(branch, path, content, message string) error {
	// Получаем текущий SHA файла если он существует
	var existing struct{ SHA string `json:"sha"` }
	var existingFile struct {
		SHA string `json:"sha"`
	}
	_ = c.get(fmt.Sprintf("/contents/%s?ref=%s", path, branch), &existingFile)

	body := map[string]any{
		"message": message,
		"content": base64.StdEncoding.EncodeToString([]byte(content)),
		"branch":  branch,
	}
	if existingFile.SHA != "" {
		body["sha"] = existingFile.SHA
	}
	_ = existing

	return c.put(fmt.Sprintf("/contents/%s", path), body, nil)
}

func (c *Client) ListFiles(dirPath, branch string) ([]string, error) {
	var items []struct {
		Path string `json:"path"`
		Type string `json:"type"`
	}
	url := fmt.Sprintf("/contents/%s?ref=%s", dirPath, branch)
	if err := c.get(url, &items); err != nil {
		return nil, err
	}
	var files []string
	for _, item := range items {
		if item.Type == "file" {
			files = append(files, item.Path)
		} else {
			files = append(files, item.Path+"/")
		}
	}
	return files, nil
}

// --- PRs ---

type PR struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"html_url"`
}

func (c *Client) CreatePR(branch, title, body string) (int, string, error) {
	payload := map[string]string{
		"title": title,
		"head":  branch,
		"base":  "main",
		"body":  body,
	}
	var pr PR
	if err := c.post("/pulls", payload, &pr); err != nil {
		return 0, "", err
	}
	return pr.Number, pr.URL, nil
}

func (c *Client) ListOpenPRs() ([]PR, error) {
	var prs []PR
	if err := c.get("/pulls?state=open", &prs); err != nil {
		return nil, err
	}
	return prs, nil
}

func (c *Client) MergePR(number int) error {
	body := map[string]string{
		"merge_method": "squash",
	}
	return c.post(fmt.Sprintf("/pulls/%d/merge", number), body, nil)
}

func (c *Client) ClosePR(number int) error {
	body := map[string]string{"state": "closed"}
	return c.patch(fmt.Sprintf("/pulls/%d", number), body, nil)
}

// --- CI Checks ---

type CheckResult struct {
	Status string // "success" | "failure" | "pending" | "timeout"
}

func (c *Client) WatchChecks(prNumber int) CheckResult {
	// PR сначала нужно получить его head SHA
	var pr struct {
		Head struct{ SHA string } `json:"head"`
	}
	if err := c.get(fmt.Sprintf("/pulls/%d", prNumber), &pr); err != nil {
		return CheckResult{Status: "failure"}
	}

	timeout := time.After(20 * time.Minute)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			return CheckResult{Status: "timeout"}
		case <-ticker.C:
			status := c.getCheckStatus(pr.Head.SHA)
			if status == "success" || status == "failure" {
				return CheckResult{Status: status}
			}
		}
	}
}

func (c *Client) getCheckStatus(sha string) string {
	var result struct {
		State string `json:"state"` // pending, success, failure, error
	}
	if err := c.get(fmt.Sprintf("/commits/%s/status", sha), &result); err != nil {
		return "pending"
	}
	switch result.State {
	case "success":
		return "success"
	case "failure", "error":
		return "failure"
	}
	return "pending"
}

func (c *Client) GetFailedCheckLog(prNumber int) string {
	// Получаем последний run из Actions для этого PR
	var runs struct {
		WorkflowRuns []struct {
			ID     int    `json:"id"`
			Status string `json:"status"`
		} `json:"workflow_runs"`
	}
	_ = c.get(fmt.Sprintf("/actions/runs?event=pull_request&per_page=5"), &runs)

	for _, run := range runs.WorkflowRuns {
		if run.Status == "completed" {
			// Получаем логи первого упавшего job
			var jobs struct {
				Jobs []struct {
					ID         int    `json:"id"`
					Conclusion string `json:"conclusion"`
				} `json:"jobs"`
			}
			_ = c.get(fmt.Sprintf("/actions/runs/%d/jobs", run.ID), &jobs)
			for _, job := range jobs.Jobs {
				if job.Conclusion == "failure" {
					log, _ := c.getJobLog(job.ID)
					return log
				}
			}
		}
	}
	return "Could not retrieve logs"
}

func (c *Client) getJobLog(jobID int) (string, error) {
	req, _ := http.NewRequest("GET",
		fmt.Sprintf("%s/actions/jobs/%d/logs", c.base, jobID), nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	// Берём последние 3000 символов (самый важный хвост лога)
	s := string(body)
	if len(s) > 3000 {
		s = s[len(s)-3000:]
	}
	return s, nil
}

// --- HTTP helpers ---

func (c *Client) get(path string, out any) error {
	req, _ := http.NewRequest("GET", c.base+path, nil)
	return c.do(req, out)
}

func (c *Client) post(path string, body, out any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", c.base+path, bytes.NewReader(b))
	return c.do(req, out)
}

func (c *Client) put(path string, body, out any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("PUT", c.base+path, bytes.NewReader(b))
	return c.do(req, out)
}

func (c *Client) patch(path string, body, out any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("PATCH", c.base+path, bytes.NewReader(b))
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitHub API %d: %s", resp.StatusCode, string(body))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func replaceAll(s, old, new string) string {
	result := ""
	for _, c := range s {
		if string(c) == old {
			result += new
		} else {
			result += string(c)
		}
	}
	return result
}
