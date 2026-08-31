package lock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// Lock represents an active stack lock and session metadata stored in S3.
type Lock struct {
	Owner         string    `json:"owner"`
	Repo          string    `json:"repo"`
	PRNumber      int       `json:"pr_number"`
	Stack         string    `json:"stack"`
	Branch        string    `json:"branch"`
	HeadSHA       string    `json:"head_sha"`
	LockedBy      string    `json:"locked_by"`
	Status        string    `json:"status"` // "previewed", "applied"
	WorkspacePath string    `json:"workspace_path"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Manager manages stack locking and session tracking via an S3 state bucket.
type Manager struct {
	s3Client *s3.Client
	bucket   string
	region   string
	baseDir  string
}

// NewManager creates a new S3 Lock Manager using AWS default credentials.
func NewManager(ctx context.Context, bucket string, baseDir string) (*Manager, error) {
	if bucket == "" {
		bucket = os.Getenv("STATE_BUCKET")
		if bucket == "" {
			bucket = os.Getenv("BABYLON_S3_BUCKET")
		}
	}
	if baseDir == "" {
		baseDir = "/tmp/babylon/workspaces"
	}

	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}

	var opts []func(*config.LoadOptions) error
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}

	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS SDK config: %w", err)
	}

	s3Client := s3.NewFromConfig(cfg)
	currentRegion := cfg.Region

	// Automatically detect the bucket's region if bucket is provided
	if bucket != "" {
		detectedRegion, err := manager.GetBucketRegion(ctx, s3Client, bucket)
		if err == nil && detectedRegion != "" && detectedRegion != cfg.Region {
			log.Printf("Auto-detected S3 bucket '%s' region: %s", bucket, detectedRegion)
			currentRegion = detectedRegion
			cfg.Region = detectedRegion
			s3Client = s3.NewFromConfig(cfg)
		} else if err != nil {
			log.Printf("Note: bucket region discovery for '%s': %v (using region: %s)", bucket, err, currentRegion)
		}
	}

	return &Manager{
		s3Client: s3Client,
		bucket:   bucket,
		region:   currentRegion,
		baseDir:  baseDir,
	}, nil
}

// Bucket returns the configured S3 bucket name.
func (m *Manager) Bucket() string {
	return m.bucket
}

// Region returns the resolved AWS region.
func (m *Manager) Region() string {
	return m.region
}

func (m *Manager) lockKey(owner, repo, stack string) string {
	return fmt.Sprintf(".pulumi/locks/%s/%s/%s.lock.json", owner, repo, stack)
}

func (m *Manager) repoLocksPrefix(owner, repo string) string {
	return fmt.Sprintf(".pulumi/locks/%s/%s/", owner, repo)
}

// GetWorkspacePath returns the standard workspace directory path on disk.
func (m *Manager) GetWorkspacePath(owner, repo string, prNum int, stack string) string {
	return filepath.Join(m.baseDir, owner, repo, fmt.Sprintf("pr-%d", prNum), stack)
}

// GetLock retrieves the lock file from S3. Returns nil if the stack is unlocked.
func (m *Manager) GetLock(ctx context.Context, owner, repo, stack string) (*Lock, error) {
	if m.bucket == "" {
		return nil, fmt.Errorf("STATE_BUCKET environment variable is not configured")
	}

	key := m.lockKey(owner, repo, stack)
	resp, err := m.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(m.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			if apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "NotFound" {
				return nil, nil // No lock file in S3
			}
		}
		var notFoundErr *s3types.NoSuchKey
		if errors.As(err, &notFoundErr) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get S3 lock object: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read S3 lock body: %w", err)
	}

	var lock Lock
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, fmt.Errorf("failed to parse S3 lock JSON: %w", err)
	}
	return &lock, nil
}

// SaveLock stores or updates the lock JSON file in S3.
func (m *Manager) SaveLock(ctx context.Context, lock *Lock) error {
	if m.bucket == "" {
		return fmt.Errorf("STATE_BUCKET environment variable is not configured")
	}

	lock.UpdatedAt = time.Now().UTC()
	if lock.CreatedAt.IsZero() {
		lock.CreatedAt = lock.UpdatedAt
	}

	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal lock: %w", err)
	}

	key := m.lockKey(lock.Owner, lock.Repo, lock.Stack)
	log.Printf("Saving S3 lock to s3://%s/%s ...", m.bucket, key)
	_, err = m.s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(m.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		log.Printf("ERROR: PutObject s3://%s/%s failed: %v", m.bucket, key, err)
		return fmt.Errorf("failed to put S3 lock object: %w", err)
	}

	log.Printf("Successfully saved S3 Lock: s3://%s/%s (PR #%d, Status: %s)", m.bucket, key, lock.PRNumber, lock.Status)
	return nil
}

// DeleteLock removes the stack lock file from S3 and cleans up the local workspace folder.
func (m *Manager) DeleteLock(ctx context.Context, owner, repo string, prNum int, stack string) error {
	if m.bucket != "" {
		key := m.lockKey(owner, repo, stack)
		_, err := m.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(m.bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			log.Printf("Warning: failed to delete S3 lock: %v", err)
		} else {
			log.Printf("S3 Lock deleted: s3://%s/%s", m.bucket, key)
		}
	}

	workspacePath := m.GetWorkspacePath(owner, repo, prNum, stack)
	_ = os.RemoveAll(workspacePath)
	return nil
}

// DeleteAllPRLocks removes all S3 locks owned by the given PR and cleans up its workspace directory.
func (m *Manager) DeleteAllPRLocks(ctx context.Context, owner, repo string, prNum int) error {
	if m.bucket != "" {
		prefix := m.repoLocksPrefix(owner, repo)
		listResp, err := m.s3Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: aws.String(m.bucket),
			Prefix: aws.String(prefix),
		})
		if err != nil {
			log.Printf("Warning: failed to list S3 locks for cleanup: %v", err)
		} else {
			for _, obj := range listResp.Contents {
				lock, err := m.getLockByKey(ctx, *obj.Key)
				if err == nil && lock != nil && lock.PRNumber == prNum {
					_, _ = m.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
						Bucket: aws.String(m.bucket),
						Key:    obj.Key,
					})
					log.Printf("Deleted S3 lock for closed PR #%d: s3://%s/%s", prNum, m.bucket, *obj.Key)
				}
			}
		}
	}

	prWorkspaceRoot := filepath.Join(m.baseDir, owner, repo, fmt.Sprintf("pr-%d", prNum))
	_ = os.RemoveAll(prWorkspaceRoot)
	log.Printf("Cleaned up workspace on disk for PR #%d: %s", prNum, prWorkspaceRoot)
	return nil
}

func (m *Manager) getLockByKey(ctx context.Context, key string) (*Lock, error) {
	resp, err := m.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(m.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var lock Lock
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, err
	}
	return &lock, nil
}
