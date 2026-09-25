// Package routing owns provider/account selection and Fusion orchestration.
package routing

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// FusionConfig bounds one Fusion request (SPEC §16). Negative limits are
// rejected; zero uses the default except Grace, where zero stops at quorum.
type FusionConfig struct {
	// MinPanelQuorum is the successful panel count required before a judge
	// synthesis replaces the direct answer.
	MinPanelQuorum int
	// Grace is how long to wait for stragglers after quorum is reached.
	Grace time.Duration
	// HardTimeout caps the whole panel phase.
	HardTimeout time.Duration
	// MaxResponseBytes caps bytes collected from one panel call.
	MaxResponseBytes int64
	// MaxPanels bounds fan-out count.
	MaxPanels int
	// MaxConcurrent caps simultaneous Fusion requests for this orchestrator.
	MaxConcurrent int
}

// DefaultFusionConfig is the minimum viable Fusion budget.
func DefaultFusionConfig() FusionConfig {
	return FusionConfig{MinPanelQuorum: 1, Grace: 250 * time.Millisecond, HardTimeout: 30 * time.Second, MaxResponseBytes: 1 << 20, MaxPanels: 8, MaxConcurrent: 8}
}

// PanelCandidate is one Fusion panel route target.
type PanelCandidate struct {
	ProviderID string
	ModelID    string
}

// PanelResult is one panel attempt outcome. Err is nil for a usable answer.
type PanelResult struct {
	Candidate PanelCandidate
	Answer    string
	Err       error
}

// PanelFunc runs one panel call. Implementations must force non-streaming,
// remove tools and flatten prior tool history before dispatch (SPEC §16).
type PanelFunc func(ctx context.Context, candidate PanelCandidate) (io.ReadCloser, error)

// JudgeFunc synthesizes 2+ panel answers into one response while preserving
// the client's own stream/tools behavior (SPEC §16).
type JudgeFunc func(ctx context.Context, judge string, answers []string) (string, error)

// Fusion orchestrates one panel fan-out and judge synthesis.
type Fusion struct {
	Config FusionConfig
	// Panels runs each panel candidate.
	Panels PanelFunc
	// Judge runs the synthesis call. Required only when 2+ panels succeed.
	Judge JudgeFunc
	// DefaultJudge is the effective judge when none is configured; callers pass
	// the first Combo model here (SPEC §16).
	DefaultJudge string

	mu           sync.Mutex
	semaphore    chan struct{}
	started      bool
	config       FusionConfig
	panels       PanelFunc
	judgeFn      JudgeFunc
	defaultJudge string
}

// Run executes the Fusion flow. A zero-success panel phase returns
// ErrFusionNoPanels; one success returns that answer directly.
func (f *Fusion) Run(ctx context.Context, candidates []PanelCandidate, judge string) (string, error) {
	if len(candidates) == 0 {
		return "", ErrFusionNoPanels
	}
	config, err := f.freezeConfig()
	if err != nil {
		return "", err
	}
	if len(candidates) > config.MaxPanels {
		return "", ErrFusionPanelLimit
	}
	if judge == "" {
		judge = f.defaultJudge
	}
	if f.panels == nil {
		return "", errors.New("fusion panel runner is required")
	}
	if !f.acquire(config.MaxConcurrent) {
		return "", ErrFusionBusy
	}
	defer f.release()

	panelCtx, cancel := context.WithTimeout(ctx, config.HardTimeout)
	defer cancel()
	results := f.fanOut(panelCtx, candidates, config.MaxResponseBytes)
	answers, graceTimer := collectAnswers(panelCtx, results, config)
	if graceTimer != nil {
		defer graceTimer.Stop()
	}
	if len(answers) == 0 {
		return "", ErrFusionNoPanels
	}
	if len(answers) < config.MinPanelQuorum {
		return "", ErrFusionQuorum
	}
	if len(answers) == 1 {
		return answers[0], nil
	}
	if f.judgeFn == nil {
		return "", errors.New("fusion judge is required for multiple panel answers")
	}
	return f.judgeFn(ctx, judge, answers)
}

// ErrFusionNoPanels reports that every panel call failed or timed out.
var ErrFusionNoPanels = errors.New("fusion: no panel produced an answer")

// ErrFusionQuorum reports fewer successes than the configured quorum.
var ErrFusionQuorum = errors.New("fusion: panel quorum not met")

// ErrFusionBusy reports that the concurrency cap rejected this request.
var ErrFusionBusy = errors.New("fusion: concurrency limit reached")

// ErrFusionPanelLimit rejects fan-out larger than the configured budget.
var ErrFusionPanelLimit = errors.New("fusion: panel limit exceeded")

// ErrFusionResponseTooLarge rejects a panel answer beyond the byte budget.
var ErrFusionResponseTooLarge = errors.New("fusion: panel response exceeded byte limit")

func (f *Fusion) fanOut(ctx context.Context, candidates []PanelCandidate, maxResponseBytes int64) <-chan PanelResult {
	results := make(chan PanelResult, len(candidates))
	var wg sync.WaitGroup
	for _, candidate := range candidates {
		wg.Add(1)
		go func(candidate PanelCandidate) {
			defer wg.Done()
			answer, err := readPanel(ctx, f.panels, candidate, maxResponseBytes)
			select {
			case results <- PanelResult{Candidate: candidate, Answer: answer, Err: err}:
			case <-ctx.Done():
			}
		}(candidate)
	}
	go func() { wg.Wait(); close(results) }()
	return results
}

// readPanel streams one panel answer and stops at the byte budget so an
// oversized upstream cannot be fully buffered before rejection.
func readPanel(ctx context.Context, panel PanelFunc, candidate PanelCandidate, maxResponseBytes int64) (string, error) {
	body, err := panel(ctx, candidate)
	if err != nil {
		return "", err
	}
	defer body.Close()
	var reader io.Reader = body
	if maxResponseBytes > 0 {
		reader = io.LimitReader(body, maxResponseBytes+1)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	if maxResponseBytes > 0 && int64(len(data)) > maxResponseBytes {
		return "", ErrFusionResponseTooLarge
	}
	return string(data), nil
}

// collectAnswers gathers successes until the hard timeout, letting stragglers
// finish during the grace window once quorum is satisfied.
func collectAnswers(ctx context.Context, results <-chan PanelResult, config FusionConfig) ([]string, *time.Timer) {
	answers := make([]string, 0, len(results))
	var graceTimer *time.Timer
	var grace <-chan time.Time
	for {
		select {
		case result, ok := <-results:
			if !ok {
				return answers, graceTimer
			}
			if result.Err == nil {
				answers = append(answers, result.Answer)
				if len(answers) >= config.MinPanelQuorum {
					if config.Grace == 0 {
						// Zero grace stops immediately at quorum.
						return answers, graceTimer
					}
					if graceTimer == nil {
						graceTimer = time.NewTimer(config.Grace)
						grace = graceTimer.C
					}
				}
			}
		case <-grace:
			return answers, graceTimer
		case <-ctx.Done():
			return answers, graceTimer
		}
	}
}

func (f *Fusion) acquire(limit int) bool {
	if limit <= 0 {
		return true
	}
	select {
	case f.semaphore <- struct{}{}:
		return true
	default:
		return false
	}
}

// freezeConfig validates and caches the effective limits on first use so a
// concurrent Config mutation cannot race with in-flight requests (SPEC §16).
func (f *Fusion) freezeConfig() (FusionConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.started {
		return f.config, nil
	}
	config := f.Config
	if config.MinPanelQuorum < 0 || config.HardTimeout < 0 || config.MaxResponseBytes < 0 || config.MaxPanels < 0 || config.MaxConcurrent < 0 || config.Grace < 0 {
		return FusionConfig{}, errors.New("fusion: limits must not be negative")
	}
	if config.MinPanelQuorum == 0 {
		config.MinPanelQuorum = 1
	}
	if config.HardTimeout == 0 {
		config.HardTimeout = DefaultFusionConfig().HardTimeout
	}
	if config.MaxPanels == 0 {
		config.MaxPanels = DefaultFusionConfig().MaxPanels
	}
	if config.MaxResponseBytes == 0 {
		config.MaxResponseBytes = DefaultFusionConfig().MaxResponseBytes
	}
	if config.MaxConcurrent == 0 {
		config.MaxConcurrent = DefaultFusionConfig().MaxConcurrent
	}
	if config.MaxConcurrent > 0 {
		f.semaphore = make(chan struct{}, config.MaxConcurrent)
	}
	f.config = config
	f.panels = f.Panels
	f.judgeFn = f.Judge
	f.defaultJudge = f.DefaultJudge
	f.started = true
	return config, nil
}

func (f *Fusion) release() {
	if f.semaphore == nil {
		return
	}
	select {
	case <-f.semaphore:
	default:
	}
}

// FlattenToolHistory renders prior tool call/result turns as prose so panel
// requests do not require native tool support (SPEC §16).
func FlattenToolHistory(turns []string) string {
	flattened := ""
	for _, turn := range turns {
		if turn == "" {
			continue
		}
		if flattened != "" {
			flattened += "\n"
		}
		flattened += turn
	}
	return flattened
}

// StripTools drops tool definitions from a request body for panel calls.
func StripTools(body map[string]any) map[string]any {
	delete(body, "tools")
	delete(body, "tool_choice")
	return body
}
