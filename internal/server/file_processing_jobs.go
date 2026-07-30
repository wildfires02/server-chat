package server

import (
	"encoding/json"
	"errors"
	"sort"
	"time"

	"chat/server/store"
	"chat/server/store/types"
)

const fileProcessingJobPrefix = "filejob:"

type persistentFileProcessingJob struct {
	ID            string         `json:"id"`
	File          *types.FileDef `json:"file"`
	URL           string         `json:"url"`
	Status        string         `json:"status"`
	Attempts      int            `json:"attempts"`
	NextAttemptAt time.Time      `json:"next_attempt_at"`
	LeaseOwner    string         `json:"lease_owner,omitempty"`
	LeaseUntil    time.Time      `json:"lease_until,omitempty"`
	LastError     string         `json:"last_error,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

type claimedFileProcessingJob struct {
	job *persistentFileProcessingJob
	raw string
}

func enqueuePersistentFileProcessingJob(definition *types.FileDef, rawURL string) error {
	if definition == nil || types.ParseUid(definition.Id).IsZero() || rawURL == "" {
		return types.ErrMalformed
	}
	now := types.TimeNow()
	job := persistentFileProcessingJob{
		ID: definition.Id, File: definition, URL: rawURL, Status: "queued",
		NextAttemptAt: now, CreatedAt: now, UpdatedAt: now,
	}
	raw, err := json.Marshal(job)
	if err != nil {
		return err
	}
	err = store.PCache.Upsert(fileProcessingJobPrefix+job.ID, string(raw), true)
	if !errors.Is(err, types.ErrDuplicate) {
		return err
	}
	key := fileProcessingJobPrefix + job.ID
	for attempt := 0; attempt < 4; attempt++ {
		currentRaw, getErr := store.PCache.Get(key)
		if getErr != nil {
			return getErr
		}
		var current persistentFileProcessingJob
		if jsonErr := json.Unmarshal([]byte(currentRaw), &current); jsonErr != nil {
			return jsonErr
		}
		switch current.Status {
		case "queued", "retrying", "processing":
			return nil
		}
		current.File = definition
		current.URL = rawURL
		current.Status = "queued"
		current.Attempts = 0
		current.NextAttemptAt = now
		current.LeaseOwner = ""
		current.LeaseUntil = time.Time{}
		current.LastError = ""
		current.UpdatedAt = now
		nextRaw, marshalErr := json.Marshal(current)
		if marshalErr != nil {
			return marshalErr
		}
		swapped, swapErr := store.PCache.CompareAndSwap(key, currentRaw, string(nextRaw))
		if swapErr != nil {
			return swapErr
		}
		if swapped {
			return nil
		}
	}
	return types.ErrPolicy
}

func claimPersistentFileProcessingJob(
	owner string,
	now time.Time,
	leaseDuration time.Duration,
	limit int,
) (*claimedFileProcessingJob, error) {
	entries, err := store.PCache.List(fileProcessingJobPrefix, limit)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		key string
		raw string
		job persistentFileProcessingJob
	}
	candidates := make([]candidate, 0, len(entries))
	for key, raw := range entries {
		var job persistentFileProcessingJob
		if jsonErr := json.Unmarshal([]byte(raw), &job); jsonErr != nil ||
			job.ID == "" || job.File == nil {
			continue
		}
		due := (job.Status == "queued" || job.Status == "retrying") &&
			!job.NextAttemptAt.After(now)
		recoverable := job.Status == "processing" && !job.LeaseUntil.After(now)
		if due || recoverable {
			candidates = append(candidates, candidate{key: key, raw: raw, job: job})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].job.NextAttemptAt.Before(candidates[j].job.NextAttemptAt)
	})
	for _, candidate := range candidates {
		job := candidate.job
		job.Status = "processing"
		job.Attempts++
		job.LeaseOwner = owner
		job.LeaseUntil = now.Add(leaseDuration)
		job.UpdatedAt = now
		nextRaw, marshalErr := json.Marshal(job)
		if marshalErr != nil {
			return nil, marshalErr
		}
		swapped, swapErr := store.PCache.CompareAndSwap(candidate.key, candidate.raw, string(nextRaw))
		if swapErr != nil {
			return nil, swapErr
		}
		if swapped {
			return &claimedFileProcessingJob{job: &job, raw: string(nextRaw)}, nil
		}
	}
	return nil, nil
}

func completePersistentFileProcessingJob(claimed *claimedFileProcessingJob) (bool, error) {
	if claimed == nil || claimed.job == nil {
		return false, types.ErrMalformed
	}
	job := *claimed.job
	job.Status = "completed"
	job.LeaseOwner = ""
	job.LeaseUntil = time.Time{}
	job.LastError = ""
	job.UpdatedAt = types.TimeNow()
	nextRaw, err := json.Marshal(job)
	if err != nil {
		return false, err
	}
	return store.PCache.CompareAndSwap(fileProcessingJobPrefix+job.ID, claimed.raw, string(nextRaw))
}

func retryPersistentFileProcessingJob(
	claimed *claimedFileProcessingJob,
	maxAttempts int,
	retryBase time.Duration,
	processErr error,
) (bool, *persistentFileProcessingJob, error) {
	if claimed == nil || claimed.job == nil || processErr == nil {
		return false, nil, types.ErrMalformed
	}
	job := *claimed.job
	job.LeaseOwner = ""
	job.LeaseUntil = time.Time{}
	job.LastError = "server-side processing failed"
	job.UpdatedAt = types.TimeNow()
	if job.Attempts >= maxAttempts {
		job.Status = "dead"
		job.NextAttemptAt = time.Time{}
	} else {
		job.Status = "retrying"
		multiplier := 1 << min(job.Attempts-1, 8)
		job.NextAttemptAt = job.UpdatedAt.Add(time.Duration(multiplier) * retryBase)
	}
	nextRaw, err := json.Marshal(job)
	if err != nil {
		return false, nil, err
	}
	swapped, err := store.PCache.CompareAndSwap(fileProcessingJobPrefix+job.ID, claimed.raw, string(nextRaw))
	return swapped, &job, err
}
