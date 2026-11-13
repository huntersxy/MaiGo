package selfupdate

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type comparePayload struct {
	AheadBy         int `json:"ahead_by"`
	BehindBy        int `json:"behind_by"`
	TotalCommits    int `json:"total_commits"`
	MergeBaseCommit struct {
		SHA string `json:"sha"`
	} `json:"merge_base_commit"`
	Files []struct {
		Filename string `json:"filename"`
		Path     string `json:"path"`
	} `json:"files"`
	Commits []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message   string `json:"message"`
			Committer struct {
				Date string `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	} `json:"commits"`
}

func checkGit(ctx context.Context, local LocalState, root, sourceID, customPrefix string, enforcePublicOutbound bool, progress func(string)) (UpdateCheck, error) {
	selected, err := selectUpdateSource(sourceID, customPrefix, enforcePublicOutbound)
	if err != nil {
		return UpdateCheck{}, err
	}
	if selected.ID == "custom" {
		return checkGitSource(ctx, local, root, selected, progress)
	}
	order := []string{"github", "cnb"}
	if selected.ID == "cnb" {
		order = []string{"cnb", "github"}
	}
	type checkResult struct {
		check UpdateCheck
		err   error
	}
	checkCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	results := make(chan checkResult, len(order))
	for _, id := range order {
		source, selectErr := selectUpdateSource(id, "", enforcePublicOutbound)
		if selectErr != nil {
			results <- checkResult{err: selectErr}
			continue
		}
		go func(source updateSource) {
			check, checkErr := checkGitSource(checkCtx, local, root, source, progress)
			results <- checkResult{check: check, err: checkErr}
		}(source)
	}
	failures := make([]string, 0, len(order))
	for range order {
		select {
		case result := <-results:
			if result.err == nil {
				return result.check, nil
			}
			failures = append(failures, result.err.Error())
		case <-checkCtx.Done():
			failures = append(failures, checkCtx.Err().Error())
			return UpdateCheck{}, errorf("fetch_failed", "检查更新超时（60 秒）：%s", strings.Join(failures, "；"))
		}
	}
	return UpdateCheck{}, errorf("fetch_failed", "GitHub 和 CNB 检查均失败：%s", strings.Join(failures, "；"))
}

func checkGitSource(ctx context.Context, local LocalState, root string, source updateSource, progress func(string)) (UpdateCheck, error) {
	if source.APIType == "custom" {
		return checkGitWithFetch(ctx, local, root, source, progress)
	}
	started := time.Now()
	stage := "读取分支信息"
	client := newHTTPClient()
	report := func(message string) {
		stage = message
		if progress != nil {
			progress(source.APIType + "：" + message)
		}
	}
	var target string
	switch source.APIType {
	case "github":
		var branchPayload struct {
			Commit struct {
				SHA string `json:"sha"`
			} `json:"commit"`
		}
		report("读取 " + local.Branch + " 分支信息")
		branchURL := fmt.Sprintf("https://api.github.com/repos/%s/branches/%s", OfficialRepositoryPath, url.PathEscape(local.Branch))
		if err := getJSON(ctx, client, branchURL, &branchPayload); err != nil {
			return UpdateCheck{}, wrappedSourceError(source, stage, started, err)
		}
		target = branchPayload.Commit.SHA
	case "cnb":
		var branchPayload struct {
			Commit struct {
				SHA string `json:"sha"`
			} `json:"commit"`
		}
		report("读取 " + local.Branch + " 分支信息")
		branchURL := fmt.Sprintf("https://cnb.cool/Mai-with-u/maibot/-/git/branches/%s", url.PathEscape(local.Branch))
		if err := getJSON(ctx, client, branchURL, &branchPayload); err != nil {
			return UpdateCheck{}, wrappedSourceError(source, stage, started, err)
		}
		target = branchPayload.Commit.SHA
	default:
		return UpdateCheck{}, errorf("invalid_source", "不支持的信息源: %s", source.APIType)
	}
	if !validateCommitSHA(target) {
		return UpdateCheck{}, wrappedSourceError(source, stage, started, errorf("fetch_failed", "无效的远端提交 SHA"))
	}
	now := float64(time.Now().UnixNano()) / 1e9
	check := UpdateCheck{
		Local: local, CheckedAt: &now, LatestCommit: target,
		Source: &UpdateSource{ID: source.ID, Name: source.Name + "（版本检查：" + source.APIType + "）", URL: source.CloneURL},
	}
	if len(local.DirtyFiles) > 0 {
		check.Blockers = append(check.Blockers, "dirty_worktree")
	}
	if target == local.Commit {
		check.LatestVersion = local.Version
		return check, nil
	}
	report("比较本地与远端提交")
	if err := compareRemote(ctx, client, source, local, target, &check); err != nil {
		return UpdateCheck{}, wrappedSourceError(source, stage, started, err)
	}
	report("读取远端版本与依赖要求")
	versionText, versionErr := remoteVersionText(ctx, client, source, target)
	if versionErr != nil {
		return UpdateCheck{}, wrappedSourceError(source, stage, started, versionErr)
	}
	check.LatestVersion, _ = readProjectVersion(versionText)
	if check.LatestVersion == "" {
		check.LatestVersion = local.Version
	}
	check.Blockers = append(check.Blockers, pythonRequirementBlockers(root, versionText)...)
	return check, nil
}

func compareRemote(ctx context.Context, client *http.Client, source updateSource, local LocalState, target string, check *UpdateCheck) error {
	var payload comparePayload
	switch source.APIType {
	case "github":
		compareURL := fmt.Sprintf("https://api.github.com/repos/%s/compare/%s...%s?per_page=100", OfficialRepositoryPath, local.Commit, target)
		if err := getJSON(ctx, client, compareURL, &payload); err != nil {
			return err
		}
		check.Behind = payload.AheadBy
		check.Ahead = payload.BehindBy
	case "cnb":
		compareURL := fmt.Sprintf("https://cnb.cool/Mai-with-u/maibot/-/git/compare/%s...%s", local.Commit, target)
		if err := getJSON(ctx, client, compareURL, &payload); err != nil {
			return err
		}
		check.Behind = payload.TotalCommits
		if payload.MergeBaseCommit.SHA != local.Commit {
			var reverse comparePayload
			reverseURL := fmt.Sprintf("https://cnb.cool/Mai-with-u/maibot/-/git/compare/%s...%s", target, local.Commit)
			if err := getJSON(ctx, client, reverseURL, &reverse); err == nil {
				check.Ahead = reverse.TotalCommits
			}
		}
	}
	if check.Ahead > 0 {
		check.Blockers = append(check.Blockers, "diverged")
	}
	changed := make(map[string]struct{})
	for _, file := range payload.Files {
		name := file.Filename
		if name == "" {
			name = file.Path
		}
		if name != "" {
			changed[name] = struct{}{}
		}
	}
	check.DependenciesChanged = dependenciesChanged(changed)
	if check.DependenciesChanged && local.DependencyTool == "" {
		check.Blockers = append(check.Blockers, "no_dependency_tool")
	}
	check.Commits = make([]UpdateCommit, 0, len(payload.Commits))
	for index, commit := range payload.Commits {
		if index >= maxListedCommits {
			break
		}
		summary := strings.SplitN(commit.Commit.Message, "\n", 2)[0]
		shortID := commit.SHA
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		check.Commits = append(check.Commits, UpdateCommit{ShortID: shortID, Summary: summary, CommittedAt: commit.Commit.Committer.Date})
	}
	return nil
}

func checkGitWithFetch(ctx context.Context, local LocalState, root string, source updateSource, progress func(string)) (UpdateCheck, error) {
	if progress != nil {
		progress("自定义源：" + "读取分支信息")
	}
	target, err := gitFetchBranch(ctx, root, source.ID, source.CloneURL, local.Branch)
	if err != nil {
		return UpdateCheck{}, err
	}
	versionText, versionErr := gitShowFile(ctx, root, target, "pyproject.toml")
	if versionErr != nil {
		versionText, _ = gitShowFile(ctx, root, target, "internal/common/version/version.go")
	}
	check, err := evaluateGitUpdate(ctx, local, root, target, versionText, source)
	if err != nil {
		return UpdateCheck{}, err
	}
	return check, nil
}

func evaluateGitUpdate(ctx context.Context, local LocalState, root, target, versionText string, source updateSource) (UpdateCheck, error) {
	now := float64(time.Now().UnixNano()) / 1e9
	check := UpdateCheck{
		Local: local, CheckedAt: &now, LatestCommit: target,
		Source: publicUpdateSource(source),
	}
	if local.Commit == "" {
		return UpdateCheck{}, errorf("state_changed", "本地仓库状态缺少当前提交，无法评估更新")
	}
	countOutput, err := gitOutput(root, gitDefaultTimeout, "rev-list", "--left-right", "--count", local.Commit+"..."+target)
	if err != nil {
		return UpdateCheck{}, err
	}
	fields := strings.Fields(countOutput)
	if len(fields) >= 2 {
		check.Ahead, _ = strconv.Atoi(fields[0])
		check.Behind, _ = strconv.Atoi(fields[1])
	}
	changed, err := changedFiles(ctx, root, local.Commit, target)
	if err != nil {
		return UpdateCheck{}, err
	}
	check.DependenciesChanged = dependenciesChanged(changed)
	commits, err := listCommits(ctx, root, local.Commit, target)
	if err != nil {
		return UpdateCheck{}, err
	}
	check.Commits = commits
	if len(local.DirtyFiles) > 0 {
		check.Blockers = append(check.Blockers, "dirty_worktree")
	}
	ancestor, err := isAncestor(ctx, root, "HEAD", target)
	if err != nil {
		return UpdateCheck{}, err
	}
	if !ancestor {
		check.Blockers = append(check.Blockers, "diverged")
	}
	if check.DependenciesChanged && local.DependencyTool == "" {
		check.Blockers = append(check.Blockers, "no_dependency_tool")
	}
	check.Blockers = append(check.Blockers, pythonRequirementBlockers(root, versionText)...)
	check.LatestVersion, _ = readProjectVersion(versionText)
	if check.LatestVersion == "" {
		check.LatestVersion = local.Version
	}
	return check, nil
}

func pythonRequirementBlockers(root, remoteText string) []string {
	localRaw, err := readLocalFile(root, "pyproject.toml")
	if err != nil {
		return nil
	}
	_, localRequires := readProjectVersion(localRaw)
	_, remoteRequires := readProjectVersion(remoteText)
	if localRequires != remoteRequires {
		return []string{"python_requirement_changed"}
	}
	return nil
}

func dependenciesChanged(changed map[string]struct{}) bool {
	for path := range changed {
		if _, ok := DependencyFiles[path]; ok {
			return true
		}
	}
	return false
}

func wrappedSourceError(source updateSource, stage string, started time.Time, err error) error {
	if value, ok := err.(*Error); ok && value.Code == "fetch_failed" {
		return value
	}
	return errorf("fetch_failed", "%s 检查失败（%s，已耗时 %.1f 秒）：%v", source.APIType, stage, time.Since(started).Seconds(), err)
}
