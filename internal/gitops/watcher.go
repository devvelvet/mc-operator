package gitops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Watcher polls a local Git working copy, pulling from origin and emitting
// classified diff summaries whenever HEAD advances.
//
// Scope: the watcher handles Git plumbing only. It does not know about
// manifests or pipelines — the reconciler subscribes to Events and decides
// what to do with each summary.
type Watcher struct {
	RepoPath   string
	RemoteName string        // default "origin"
	Branch     string        // default "main"
	Interval   time.Duration // default 30s
	Classifier *DiffClassifier

	// Events is where new diff summaries are delivered.
	Events chan<- WatchEvent
}

// WatchEvent is a single observation delivered by the watcher.
type WatchEvent struct {
	Time     time.Time
	Commit   string
	Previous string
	Summary  DiffSummary
	Err      error
}

// Run blocks until ctx is cancelled, pulling on the configured interval.
// On startup it performs an initial pull so any in-flight changes are noticed.
func (w *Watcher) Run(ctx context.Context) error {
	if w.RemoteName == "" {
		w.RemoteName = "origin"
	}
	if w.Branch == "" {
		w.Branch = "main"
	}
	if w.Interval <= 0 {
		w.Interval = 30 * time.Second
	}
	if w.Classifier == nil {
		w.Classifier = DefaultClassifier()
	}
	if _, err := os.Stat(w.RepoPath); err != nil {
		return fmt.Errorf("repo path: %w", err)
	}

	repo, err := git.PlainOpen(w.RepoPath)
	if err != nil {
		return fmt.Errorf("open repo: %w", err)
	}

	// Establish baseline HEAD.
	prevHead, err := headCommit(repo)
	if err != nil {
		return fmt.Errorf("initial head: %w", err)
	}

	tick := time.NewTicker(w.Interval)
	defer tick.Stop()

	run := func() {
		newHead, summary, err := w.pullAndDiff(ctx, repo, prevHead)
		if err != nil {
			w.emit(WatchEvent{Time: time.Now().UTC(), Err: err})
			return
		}
		if newHead == prevHead {
			return // no change
		}
		w.emit(WatchEvent{
			Time:     time.Now().UTC(),
			Commit:   newHead,
			Previous: prevHead,
			Summary:  summary,
		})
		prevHead = newHead
	}

	// Initial pass so an already-up-to-date repo still produces a baseline event.
	run()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			run()
		}
	}
}

func (w *Watcher) emit(ev WatchEvent) {
	if w.Events == nil {
		return
	}
	select {
	case w.Events <- ev:
	case <-time.After(100 * time.Millisecond):
		// drop on backpressure
	}
}

// pullAndDiff fetches from origin (when one is configured), fast-forwards the
// working copy, and returns (new HEAD sha, classified diff summary).
//
// Repos without an `origin` remote are still supported: the watcher simply
// skips the pull and only reacts to local commits. This matches the common
// dev workflow of `git init` + manual edits.
func (w *Watcher) pullAndDiff(ctx context.Context, repo *git.Repository, prevHead string) (string, DiffSummary, error) {
	wt, err := repo.Worktree()
	if err != nil {
		return "", DiffSummary{}, err
	}
	if _, remoteErr := repo.Remote(w.RemoteName); remoteErr == nil {
		err = wt.PullContext(ctx, &git.PullOptions{
			RemoteName:    w.RemoteName,
			ReferenceName: plumbing.NewBranchReferenceName(w.Branch),
			SingleBranch:  true,
		})
		if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
			return "", DiffSummary{}, fmt.Errorf("pull: %w", err)
		}
	}
	// If no remote is configured, fall through to local HEAD comparison.

	newHead, err := headCommit(repo)
	if err != nil {
		return "", DiffSummary{}, err
	}
	if newHead == prevHead {
		return newHead, DiffSummary{}, nil
	}

	paths, err := changedPaths(repo, prevHead, newHead)
	if err != nil {
		return "", DiffSummary{}, err
	}
	summary := Summarize(w.Classifier.ClassifyAll(paths))
	return newHead, summary, nil
}

func headCommit(repo *git.Repository) (string, error) {
	ref, err := repo.Head()
	if err != nil {
		return "", err
	}
	return ref.Hash().String(), nil
}

// changedPaths returns the list of file paths that differ between two commits.
func changedPaths(repo *git.Repository, fromSha, toSha string) ([]string, error) {
	if fromSha == "" || fromSha == toSha {
		return nil, nil
	}
	from, err := repo.CommitObject(plumbing.NewHash(fromSha))
	if err != nil {
		return nil, err
	}
	to, err := repo.CommitObject(plumbing.NewHash(toSha))
	if err != nil {
		return nil, err
	}
	patch, err := from.Patch(to)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, fp := range patch.FilePatches() {
		from, to := fp.Files()
		add := func(f object.File) {
			if f.Name != "" {
				seen[f.Name] = struct{}{}
			}
		}
		if from != nil {
			add(object.File{Name: from.Path()})
		}
		if to != nil {
			add(object.File{Name: to.Path()})
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	return out, nil
}
