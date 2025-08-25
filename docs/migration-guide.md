# Migration Guide

Guide for migrating between different versions of the LiveKit Agent SDK.

## Table of Contents

- [Overview](#overview)
- [Version Compatibility](#version-compatibility)
- [Migration from 0.0.x to 0.1.x](#migration-from-00x-to-01x)
- [Breaking Changes](#breaking-changes)
- [Best Practices for Upgrades](#best-practices-for-upgrades)
- [Rollback Procedures](#rollback-procedures)
- [Version-Specific Notes](#version-specific-notes)

## Overview

The LiveKit Agent SDK follows [semantic versioning](https://semver.org/):

- **Major versions** (1.0.0, 2.0.0): Breaking API changes
- **Minor versions** (0.1.0, 0.2.0): New features, backward compatible
- **Patch versions** (0.1.1, 0.1.2): Bug fixes, backward compatible

This guide helps you migrate between versions safely and efficiently.

## Version Compatibility

### Current Version Support

| Agent SDK Version | LiveKit Server Version | Go Version | Status |
|---|---|---|---|
| 1.0.x (UniversalWorker) | 1.6.x+ | 1.21+ | Current |
| 0.2.x (deprecated workers) | 1.5.x+ | 1.21+ | Deprecated |
| 0.1.x | 1.4.x+ | 1.21+ | End of Life |
| 0.0.x | 1.3.x+ | 1.20+ | End of Life |

### Deprecation Timeline

- **0.0.x**: End of life
- **0.1.x**: End of life
- **0.2.x**: Deprecated - Worker, ParticipantAgent, PublisherAgent are deprecated
- **1.0.x**: Current stable release with UniversalWorker and UniversalHandler interface

## Migration to UniversalWorker (1.0.x)

### Key Changes from Deprecated Workers

1. **Unified Worker**: UniversalWorker replaces Worker, ParticipantAgent, and PublisherAgent
2. **New Handler Interface**: UniversalHandler with optional participant event callbacks
3. **JobContext**: OnJobAssigned now receives JobContext instead of separate job and room parameters
4. **SimpleUniversalHandler**: Convenience handler with function callbacks for easier implementation
5. **Direct Event Handling**: Built-in participant event callbacks instead of polling patterns

### Step-by-Step Migration

#### 1. Update Dependencies

```bash
# Update go.mod
go get github.com/am-sokolov/livekit-agent-sdk-go@latest

# Clean module cache if needed
go clean -modcache
go mod download
```

#### 2. Update from Deprecated Workers to UniversalWorker

**Before (deprecated Worker):**
```go
type MyHandler struct {}

func (h *MyHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
    return true, &agent.JobMetadata{
        ParticipantIdentity: "my-agent",
    }
}

func (h *MyHandler) OnJobAssigned(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    // Handle job with separate parameters
    return nil
}

func (h *MyHandler) OnJobTerminated(ctx context.Context, jobID string) {
    // Cleanup
}

// Create worker
worker := agent.NewWorker(url, apiKey, apiSecret, handler, opts)
```

**After (UniversalWorker with SimpleUniversalHandler):**
```go
handler := &agent.SimpleUniversalHandler{
    JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
        return true, &agent.JobMetadata{
            ParticipantIdentity: "my-agent",
            ParticipantName:     "My Agent",
        }
    },
    
    JobAssignedFunc: func(ctx context.Context, jobCtx *agent.JobContext) error {
        // Access job and room through JobContext
        log.Printf("Handling job %s in room %s", jobCtx.Job.Id, jobCtx.Room.Name())
        <-ctx.Done()
        return nil
    },
    
    JobTerminatedFunc: func(ctx context.Context, jobID string) {
        log.Printf("Job %s terminated", jobID)
    },
    
    // Optional: Direct participant event handling
    ParticipantJoinedFunc: func(ctx context.Context, p *lksdk.RemoteParticipant) {
        log.Printf("Participant joined: %s", p.Identity())
    },
}

// Create UniversalWorker
worker := agent.NewUniversalWorker(url, apiKey, apiSecret, handler, opts)
```

#### 3. Migrate from ParticipantAgent/PublisherAgent

**Before (ParticipantAgent):**
```go
// Create base worker first
worker := agent.NewWorker(url, apiKey, apiSecret, nil, opts)

// Create participant agent
participantAgent := agent.NewParticipantAgent(worker, agent.ParticipantAgentOptions{
    Identity: "participant-agent",
    Metadata: `{"type": "participant"}`,
})

// Handle participant events
participantAgent.OnTrackPublished = func(pub *lksdk.RemoteTrackPublication, p *lksdk.RemoteParticipant) {
    log.Printf("Track published: %s", pub.SID())
}
```

**After (UniversalWorker):**
```go
// Single unified worker with all functionality
handler := &agent.SimpleUniversalHandler{
    JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
        return true, &agent.JobMetadata{
            ParticipantIdentity: "participant-agent",
            ParticipantMetadata: `{"type": "participant"}`,
        }
    },
    
    // Built-in track event handling
    TrackPublishedFunc: func(ctx context.Context, pub *lksdk.RemoteTrackPublication, p *lksdk.RemoteParticipant) {
        log.Printf("Track published: %s", pub.SID())
    },
}

worker := agent.NewUniversalWorker(url, apiKey, apiSecret, handler, opts)
```

#### 4. Update Room Event Handling

**Before (polling pattern with deprecated Worker):**
```go
func (h *MyHandler) OnJobAssigned(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    // Manual polling for participant changes
    go func() {
        ticker := time.NewTicker(500 * time.Millisecond)
        defer ticker.Stop()
        
        knownParticipants := make(map[string]bool)
        
        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                for _, p := range room.GetRemoteParticipants() {
                    if !knownParticipants[p.Identity()] {
                        knownParticipants[p.Identity()] = true
                        log.Printf("Participant joined: %s", p.Identity())
                    }
                }
            }
        }
    }()
    
    <-ctx.Done()
    return nil
}
```

**After (UniversalWorker with direct callbacks):**
```go
handler := &agent.SimpleUniversalHandler{
    JobAssignedFunc: func(ctx context.Context, jobCtx *agent.JobContext) error {
        // No polling needed - events handled directly
        log.Printf("Agent in room: %s", jobCtx.Room.Name())
        <-ctx.Done()
        return nil
    },
    
    // Direct participant event callbacks
    ParticipantJoinedFunc: func(ctx context.Context, p *lksdk.RemoteParticipant) {
        log.Printf("Participant joined: %s", p.Identity())
    },
    
    ParticipantLeftFunc: func(ctx context.Context, p *lksdk.RemoteParticipant) {
        log.Printf("Participant left: %s", p.Identity())
    },
    
    TrackPublishedFunc: func(ctx context.Context, pub *lksdk.RemoteTrackPublication, p *lksdk.RemoteParticipant) {
        log.Printf("Track published: %s by %s", pub.SID(), p.Identity())
    },
}
```

#### 5. Update Context Handling

**Before (0.0.x):**
```go
func (h *MyHandler) HandleJob(job *Job, room *Room) error {
    // No context support
    for {
        // Long-running work without cancellation
        doWork()
    }
}
```

**After (0.1.x):**
```go
func (h *MyHandler) OnJobAssigned(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    ticker := time.NewTicker(time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            log.Println("Job cancelled")
            return ctx.Err()
        case <-ticker.C:
            if err := doWork(); err != nil {
                return err
            }
        }
    }
}
```

#### 5. Update Error Handling

**Before (0.0.x):**
```go
func (h *MyHandler) HandleJob(job *Job, room *Room) error {
    if err := someOperation(); err != nil {
        return fmt.Errorf("operation failed: %v", err)
    }
    return nil
}
```

**After (0.1.x):**
```go
import "github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"

func (h *MyHandler) OnJobAssigned(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    if err := someOperation(); err != nil {
        if errors.Is(err, agent.ErrResourceExhausted) {
            return agent.ErrResourceExhausted
        }
        return fmt.Errorf("operation failed: %w", err)
    }
    return nil
}
```

#### 6. Update Configuration

**Before (0.0.x):**
```go
config := &WorkerConfig{
    MaxJobs:        5,
    RetryAttempts:  3,
    RetryDelay:     time.Second,
    EnableMetrics:  true,
}
```

**After (0.1.x):**
```go
options := agent.WorkerOptions{
    MaxConcurrentJobs: 5,
    EnableJobRecovery: true,
    RecoveryHandler: &agent.DefaultRecoveryHandler{
        MaxRetries:    3,
        BackoffBase:   time.Second,
    },
    ResourceLimiter: agent.NewResourceLimiter(agent.ResourceLimits{
        MaxMemoryMB:   1024,
        MaxCPUPercent: 80,
    }),
}
```

### Migration Script

Create a script to help automate the migration:

```bash
#!/bin/bash

# migrate_agent.sh - Automated migration script

echo "Migrating LiveKit Agent SDK from 0.0.x to 0.1.x"

# 1. Update go.mod
echo "Updating dependencies..."
go get github.com/am-sokolov/livekit-agent-sdk-go@latest
go mod tidy

# 2. Update imports
echo "Updating imports..."
find . -name "*.go" -exec sed -i 's|github.com/livekit/agent-sdk-go|github.com/am-sokolov/livekit-agent-sdk-go|g' {} \;

# 3. Update method signatures
echo "Updating method signatures..."
find . -name "*.go" -exec sed -i 's|HandleJob(job \*Job, room \*Room)|OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room)|g' {} \;

# 4. Add context imports where needed
echo "Adding context imports..."
find . -name "*.go" -exec grep -l "OnJob.*context.Context" {} \; | xargs -I {} bash -c '
    if ! grep -q "\"context\"" "$1"; then
        sed -i "/import (/a\\    \"context\"" "$1"
    fi
' _ {}

echo "Migration script completed. Please review changes and test thoroughly."
echo "See migration-guide.md for manual steps that may be required."
```

## Breaking Changes

### 0.1.0 Breaking Changes

1. **Job Handler Interface**
   - Changed from `HandleJob` to `OnJob`
   - Added `context.Context` parameter
   - Updated parameter types

2. **Worker Creation**
   - Simplified constructor parameters
   - New `WorkerOptions` struct
   - Removed separate config struct

3. **Error Types**
   - New structured error types
   - Wrapped errors with context
   - Standardized error handling

4. **Import Paths**
   - Changed from `github.com/livekit/agent-sdk-go` to `github.com/am-sokolov/livekit-agent-sdk-go`

5. **Configuration**
   - Consolidated configuration options
   - New resource limiting system
   - Updated job recovery mechanism

### Deprecated Features (1.0.0)

| Feature | Replacement | Status |
|---|---|---|
| `Worker` class | `UniversalWorker` | Deprecated |
| `ParticipantAgent` class | `UniversalWorker` | Deprecated |
| `PublisherAgent` class | `UniversalWorker` | Deprecated |
| `JobHandler` interface | `UniversalHandler` interface | Deprecated |
| Polling for events | Direct event callbacks | Replaced |
| Separate job/room parameters | `JobContext` parameter | Replaced |

## Best Practices for Upgrades

### Pre-Migration Checklist

1. **Backup Current Code**
   ```bash
   git checkout -b backup-before-migration
   git commit -am "Backup before Agent SDK migration"
   ```

2. **Review Dependencies**
   ```bash
   go list -m all | grep livekit
   ```

3. **Run Tests**
   ```bash
   go test ./... -v
   ```

4. **Check for Breaking Changes**
   - Review this migration guide
   - Check release notes for your target version
   - Identify deprecated features in your code

### Migration Process

1. **Create Migration Branch**
   ```bash
   git checkout -b migrate-to-v0.1.0
   ```

2. **Update Dependencies**
   ```bash
   go get github.com/am-sokolov/livekit-agent-sdk-go@latest
   go mod tidy
   ```

3. **Fix Compilation Errors**
   - Update imports
   - Update method signatures  
   - Fix type mismatches

4. **Update Logic**
   - Add context handling
   - Update error handling
   - Test functionality

5. **Test Thoroughly**
   ```bash
   go test ./...
   go build ./...
   ```

6. **Deploy to Staging**
   - Test in staging environment
   - Monitor for issues
   - Performance testing

### Post-Migration Validation

1. **Functional Testing**
   - Verify all job types work
   - Test error scenarios
   - Validate recovery mechanisms

2. **Performance Testing**
   - Compare resource usage
   - Test load handling
   - Monitor memory leaks

3. **Integration Testing**
   - Test with LiveKit server
   - Verify WebSocket connections
   - Test job dispatching

## Rollback Procedures

### Quick Rollback

If you encounter critical issues:

```bash
# 1. Revert to backup branch
git checkout backup-before-migration

# 2. Restore dependencies  
go mod tidy

# 3. Rebuild and deploy
go build ./...
```

### Selective Rollback

For partial rollbacks:

```bash
# Rollback specific files
git checkout backup-before-migration -- pkg/handler.go

# Rollback dependencies only
git checkout backup-before-migration -- go.mod go.sum
go mod tidy
```

### Production Rollback

For production environments:

1. **Immediate Actions**
   - Scale down new version
   - Scale up previous version
   - Monitor error rates

2. **Gradual Rollback**
   - Route traffic back to old version
   - Monitor system stability
   - Investigate issues

3. **Post-Rollback**
   - Document issues encountered
   - Plan fix approach
   - Schedule retry migration

## Version-Specific Notes

### 0.1.0

**Release Date**: 2024-01-15

**New Features**:
- Context-based job cancellation
- Resource limiting and monitoring
- Enhanced error handling
- Improved recovery mechanisms
- Performance optimizations

**Migration Effort**: Medium (2-4 hours for typical projects)

**Testing Requirements**: 
- Full regression testing required
- Performance testing recommended
- Load testing for production deployments

### 0.0.x (Deprecated)

**Support Status**: Security patches only until 2024-06-15

**Known Issues**:
- Memory leaks in long-running jobs
- Limited error context
- No resource monitoring
- Basic recovery mechanisms

**Recommendation**: Migrate to 0.1.x as soon as possible

## Migration Tools

### Compatibility Checker

```go
// tools/check-compatibility.go
package main

import (
    "fmt"
    "go/ast"
    "go/parser"
    "go/token"
    "os"
    "path/filepath"
    "strings"
)

func main() {
    issues := []string{}
    
    err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
        if !strings.HasSuffix(path, ".go") {
            return nil
        }
        
        fset := token.NewFileSet()
        node, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
        if err != nil {
            return err
        }
        
        ast.Inspect(node, func(n ast.Node) bool {
            switch x := n.(type) {
            case *ast.FuncDecl:
                if x.Name.Name == "HandleJob" {
                    issues = append(issues, fmt.Sprintf("%s: Found HandleJob method, should be OnJob", path))
                }
            case *ast.ImportSpec:
                if x.Path.Value == `"github.com/livekit/agent-sdk-go"` {
                    issues = append(issues, fmt.Sprintf("%s: Old import path detected", path))
                }
            }
            return true
        })
        
        return nil
    })
    
    if err != nil {
        fmt.Printf("Error scanning files: %v\n", err)
        os.Exit(1)
    }
    
    if len(issues) > 0 {
        fmt.Println("Migration issues found:")
        for _, issue := range issues {
            fmt.Printf("  - %s\n", issue)
        }
        os.Exit(1)
    } else {
        fmt.Println("No migration issues found!")
    }
}
```

### Test Migration Helper

```go
// tools/test-migration.go  
package main

import (
    "context"
    "log"
    "time"
    
    "github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
    "github.com/livekit/protocol/livekit"
    lksdk "github.com/livekit/server-sdk-go/v2"
)

// TestHandler validates that the migration worked correctly
type TestHandler struct{}

func (h *TestHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    log.Printf("✓ OnJob method signature is correct")
    log.Printf("✓ Context parameter received: %v", ctx != nil)
    log.Printf("✓ Job parameter received: %v", job != nil)
    log.Printf("✓ Room parameter received: %v", room != nil)
    
    // Test context cancellation
    select {
    case <-ctx.Done():
        log.Printf("✓ Context cancellation works")
        return ctx.Err()
    case <-time.After(100 * time.Millisecond):
        log.Printf("✓ Job processing works")
        return nil
    }
}

func main() {
    log.Println("Testing migration compatibility...")
    
    handler := &TestHandler{}
    
    // Test worker creation
    worker := agent.NewWorker(
        "ws://localhost:7880",
        "test-key", 
        "test-secret",
        handler,
        agent.WorkerOptions{
            AgentName: "migration-test",
            JobTypes:  []livekit.JobType{livekit.JobType_JT_ROOM},
        },
    )
    
    if worker != nil {
        log.Println("✓ Worker creation successful")
        log.Println("Migration validation completed successfully!")
    } else {
        log.Println("✗ Worker creation failed")
    }
}
```

## Getting Help

If you encounter issues during migration:

1. **Check Common Issues**: Review the [troubleshooting guide](troubleshooting.md)
2. **Community Support**: Join [LiveKit Slack](https://livekit.io/slack)
3. **GitHub Issues**: Report bugs or ask questions on [GitHub](https://github.com/am-sokolov/livekit-agent-sdk-go/issues)
4. **Documentation**: Review the complete [API reference](api-reference.md)

When asking for help, include:
- Current Agent SDK version
- Target Agent SDK version
- Complete error messages
- Minimal reproduction code
- Migration steps already completed