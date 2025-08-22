package helpers

import (
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/utils"
)

// JobBuilder helps create test jobs
type JobBuilder struct {
	job *livekit.Job
}

// NewJobBuilder creates a new job builder
func NewJobBuilder() *JobBuilder {
	return &JobBuilder{
		job: &livekit.Job{
			Id:   utils.NewGuid("J_"),
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{
				Sid:  utils.NewGuid("RM_"),
				Name: "test-room",
			},
			State: &livekit.JobState{
				Status: livekit.JobStatus_JS_PENDING,
			},
		},
	}
}

// WithID sets the job ID
func (b *JobBuilder) WithID(id string) *JobBuilder {
	b.job.Id = id
	return b
}

// WithType sets the job type
func (b *JobBuilder) WithType(jobType livekit.JobType) *JobBuilder {
	b.job.Type = jobType
	return b
}

// WithRoom sets the room information
func (b *JobBuilder) WithRoom(name, sid string) *JobBuilder {
	b.job.Room = &livekit.Room{
		Sid:  sid,
		Name: name,
	}
	return b
}

// WithParticipant sets the participant information
func (b *JobBuilder) WithParticipant(identity, name string) *JobBuilder {
	b.job.Participant = &livekit.ParticipantInfo{
		Sid:      utils.NewGuid("PA_"),
		Identity: identity,
		Name:     name,
	}
	return b
}

// WithAgentName sets the agent name
func (b *JobBuilder) WithAgentName(name string) *JobBuilder {
	b.job.AgentName = name
	return b
}

// WithMetadata sets the job metadata
func (b *JobBuilder) WithMetadata(metadata string) *JobBuilder {
	b.job.Metadata = metadata
	return b
}

// WithState sets the job state
func (b *JobBuilder) WithState(status livekit.JobStatus, participantIdentity string) *JobBuilder {
	b.job.State = &livekit.JobState{
		Status:              status,
		ParticipantIdentity: participantIdentity,
	}
	return b
}

// Build returns the constructed job
func (b *JobBuilder) Build() *livekit.Job {
	return b.job
}

// MessageBuilder helps create test messages
type MessageBuilder struct {
	// Common fields
	messageType string
}

// NewServerMessageBuilder creates a builder for server messages
func NewServerMessageBuilder() *MessageBuilder {
	return &MessageBuilder{messageType: "server"}
}

// NewWorkerMessageBuilder creates a builder for worker messages
func NewWorkerMessageBuilder() *MessageBuilder {
	return &MessageBuilder{messageType: "worker"}
}

// BuildRegisterRequest creates a register worker request
func (m *MessageBuilder) BuildRegisterRequest(agentName, version string, jobType livekit.JobType) *livekit.WorkerMessage {
	return &livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_Register{
			Register: &livekit.RegisterWorkerRequest{
				Type:      jobType,
				Version:   version,
				AgentName: agentName,
			},
		},
	}
}

// BuildRegisterResponse creates a register worker response
func (m *MessageBuilder) BuildRegisterResponse(workerID string) *livekit.ServerMessage {
	return &livekit.ServerMessage{
		Message: &livekit.ServerMessage_Register{
			Register: &livekit.RegisterWorkerResponse{
				WorkerId: workerID,
				ServerInfo: &livekit.ServerInfo{
					Edition:       livekit.ServerInfo_Standard,
					Version:       "test",
					Protocol:      1,
					AgentProtocol: 1,
					Region:        "test",
					NodeId:        "test-node",
				},
			},
		},
	}
}

// BuildAvailabilityRequest creates an availability request
func (m *MessageBuilder) BuildAvailabilityRequest(job *livekit.Job) *livekit.ServerMessage {
	return &livekit.ServerMessage{
		Message: &livekit.ServerMessage_Availability{
			Availability: &livekit.AvailabilityRequest{
				Job: job,
			},
		},
	}
}

// BuildAvailabilityResponse creates an availability response
func (m *MessageBuilder) BuildAvailabilityResponse(jobID string, available bool, identity string) *livekit.WorkerMessage {
	return &livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_Availability{
			Availability: &livekit.AvailabilityResponse{
				JobId:               jobID,
				Available:           available,
				ParticipantIdentity: identity,
			},
		},
	}
}

// BuildJobAssignment creates a job assignment
func (m *MessageBuilder) BuildJobAssignment(job *livekit.Job, token string) *livekit.ServerMessage {
	return &livekit.ServerMessage{
		Message: &livekit.ServerMessage_Assignment{
			Assignment: &livekit.JobAssignment{
				Job:   job,
				Token: token,
			},
		},
	}
}

// BuildJobTermination creates a job termination
func (m *MessageBuilder) BuildJobTermination(jobID string) *livekit.ServerMessage {
	return &livekit.ServerMessage{
		Message: &livekit.ServerMessage_Termination{
			Termination: &livekit.JobTermination{
				JobId:  jobID,
			},
		},
	}
}

// BuildUpdateJobStatus creates an update job status message
func (m *MessageBuilder) BuildUpdateJobStatus(jobID string, status livekit.JobStatus, error string) *livekit.WorkerMessage {
	return &livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_UpdateJob{
			UpdateJob: &livekit.UpdateJobStatus{
				JobId:  jobID,
				Status: status,
				Error:  error,
			},
		},
	}
}

// BuildUpdateWorkerStatus creates an update worker status message
func (m *MessageBuilder) BuildUpdateWorkerStatus(status livekit.WorkerStatus, load float32, jobCount uint32) *livekit.WorkerMessage {
	return &livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_UpdateWorker{
			UpdateWorker: &livekit.UpdateWorkerStatus{
				Status:   &status,
				Load:     load,
				JobCount: jobCount,
			},
		},
	}
}

// BuildPing creates a worker ping message
func (m *MessageBuilder) BuildPing(timestamp int64) *livekit.WorkerMessage {
	return &livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_Ping{
			Ping: &livekit.WorkerPing{
				Timestamp: timestamp,
			},
		},
	}
}

// BuildPong creates a server pong message
func (m *MessageBuilder) BuildPong(requestTimestamp, timestamp int64) *livekit.ServerMessage {
	return &livekit.ServerMessage{
		Message: &livekit.ServerMessage_Pong{
			Pong: &livekit.WorkerPong{
				LastTimestamp: requestTimestamp,
				Timestamp:     timestamp,
			},
		},
	}
}