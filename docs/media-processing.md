# Media Processing

This guide covers media processing capabilities in the LiveKit Agent SDK, including media pipelines, quality control, and track management.

## Media Pipeline Architecture

The SDK provides a flexible pipeline architecture for processing audio and video streams.

### Pipeline Overview

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│   Input     │ ──► │   Stage 1   │ ──► │   Stage 2   │ ──► │   Output    │
│   Buffer    │     │  (Decode)   │     │ (Process)   │     │   Buffer    │
└─────────────┘     └─────────────┘     └─────────────┘     └─────────────┘
                            │                    │
                            ▼                    ▼
                    ┌─────────────┐     ┌─────────────┐
                    │  Processor  │     │  Processor  │
                    └─────────────┘     └─────────────┘
```

### Creating a Media Pipeline

```go
// Create pipeline
pipeline := agent.NewMediaPipeline()

// Add processing stages
pipeline.AddStage(agent.NewTranscodingStage("transcode", 10, targetFormat))
pipeline.AddStage(agent.NewFilteringStage("denoise", 20))
pipeline.AddStage(agent.NewEnhancementStage("enhance", 30))

// Register custom processors
pipeline.RegisterProcessor(NewCustomAudioProcessor())
pipeline.RegisterProcessor(NewCustomVideoProcessor())

// Start processing a track
err := pipeline.StartProcessingTrack(remoteTrack)
if err != nil {
    log.Printf("Failed to start processing: %v", err)
}

// Get processed output
outputBuffer, exists := pipeline.GetOutputBuffer(remoteTrack.ID())
if exists {
    // Consume processed media
    for {
        if mediaData := outputBuffer.Dequeue(); mediaData != nil {
            // Use processed media
            handleProcessedMedia(mediaData)
        }
    }
}
```

## Custom Pipeline Stages

### Implementing a Pipeline Stage

```go
type NoiseReductionStage struct {
    name          string
    priority      int
    noiseReducer  *NoiseReducer
    noiseProfile  *NoiseProfile
}

func NewNoiseReductionStage(name string, priority int) *NoiseReductionStage {
    return &NoiseReductionStage{
        name:         name,
        priority:     priority,
        noiseReducer: NewNoiseReducer(),
        noiseProfile: NewNoiseProfile(),
    }
}

func (s *NoiseReductionStage) GetName() string {
    return s.name
}

func (s *NoiseReductionStage) GetPriority() int {
    return s.priority
}

func (s *NoiseReductionStage) CanProcess(mediaType agent.MediaType) bool {
    // Only process audio
    return mediaType == agent.MediaTypeAudio
}

func (s *NoiseReductionStage) Process(ctx context.Context, input agent.MediaData) (agent.MediaData, error) {
    // Check if audio data
    if input.Type != agent.MediaTypeAudio {
        return input, nil
    }
    
    // Apply noise reduction
    processedData, err := s.noiseReducer.Process(
        input.Data,
        input.Format.SampleRate,
        input.Format.Channels,
        s.noiseProfile,
    )
    if err != nil {
        return input, fmt.Errorf("noise reduction failed: %w", err)
    }
    
    // Return processed data
    output := input
    output.Data = processedData
    
    // Add metadata
    if output.Metadata == nil {
        output.Metadata = make(map[string]interface{})
    }
    output.Metadata["noise_reduced"] = true
    output.Metadata["noise_reduction_db"] = s.noiseReducer.GetReductionLevel()
    
    return output, nil
}
```

### Video Processing Stage

```go
type VideoEnhancementStage struct {
    name         string
    priority     int
    enhancer     *VideoEnhancer
    gpuAvailable bool
}

func (s *VideoEnhancementStage) Process(ctx context.Context, input agent.MediaData) (agent.MediaData, error) {
    if input.Type != agent.MediaTypeVideo {
        return input, nil
    }
    
    // Decode frame
    frame, err := s.decodeFrame(input.Data, input.Format)
    if err != nil {
        return input, err
    }
    
    // Apply enhancements
    enhanced := frame
    
    // Denoise
    if s.enhancer.DenoiseEnabled {
        enhanced = s.enhancer.Denoise(enhanced)
    }
    
    // Sharpen
    if s.enhancer.SharpenEnabled {
        enhanced = s.enhancer.Sharpen(enhanced, s.enhancer.SharpenStrength)
    }
    
    // Color correction
    if s.enhancer.ColorCorrectionEnabled {
        enhanced = s.enhancer.CorrectColors(enhanced)
    }
    
    // Super resolution (if GPU available)
    if s.gpuAvailable && s.enhancer.SuperResolutionEnabled {
        enhanced = s.enhancer.ApplySuperResolution(enhanced)
    }
    
    // Encode back
    encodedData, err := s.encodeFrame(enhanced, input.Format)
    if err != nil {
        return input, err
    }
    
    output := input
    output.Data = encodedData
    output.Metadata["enhanced"] = true
    
    return output, nil
}
```

## Media Processors

### Audio Processor Implementation

```go
type AudioProcessor struct {
    name         string
    capabilities agent.ProcessorCapabilities
    resampler    *Resampler
    encoder      *AudioEncoder
}

func NewAudioProcessor() *AudioProcessor {
    return &AudioProcessor{
        name: "audio-processor",
        capabilities: agent.ProcessorCapabilities{
            SupportedMediaTypes: []agent.MediaType{agent.MediaTypeAudio},
            SupportedFormats: []agent.MediaFormat{
                {SampleRate: 48000, Channels: 1, BitDepth: 16},
                {SampleRate: 48000, Channels: 2, BitDepth: 16},
                {SampleRate: 16000, Channels: 1, BitDepth: 16}, // For speech
            },
            MaxConcurrency: 4,
            RequiresGPU:    false,
        },
        resampler: NewResampler(),
        encoder:   NewAudioEncoder(),
    }
}

func (p *AudioProcessor) ProcessAudio(
    ctx context.Context,
    samples []byte,
    sampleRate uint32,
    channels uint8,
) ([]byte, error) {
    // Resample if needed
    targetRate := uint32(48000)
    if sampleRate != targetRate {
        resampled, err := p.resampler.Resample(samples, sampleRate, targetRate, channels)
        if err != nil {
            return nil, err
        }
        samples = resampled
    }
    
    // Apply audio processing
    processed := p.applyEffects(samples, targetRate, channels)
    
    // Encode if needed
    encoded, err := p.encoder.Encode(processed, targetRate, channels)
    if err != nil {
        return nil, err
    }
    
    return encoded, nil
}

func (p *AudioProcessor) applyEffects(samples []byte, sampleRate uint32, channels uint8) []byte {
    // Convert to float32 for processing
    floatSamples := bytesToFloat32(samples)
    
    // Apply gain normalization
    floatSamples = normalizeGain(floatSamples, -3.0) // -3dB target
    
    // Apply compression
    floatSamples = applyCompression(floatSamples, 4.0, -20.0) // 4:1 ratio, -20dB threshold
    
    // Apply EQ
    floatSamples = applyEQ(floatSamples, sampleRate, channels)
    
    // Convert back to bytes
    return float32ToBytes(floatSamples)
}
```

### Video Processor with ML

```go
type MLVideoProcessor struct {
    name         string
    model        *onnxruntime.Session
    preprocessor *VideoPreprocessor
    postprocessor *VideoPostprocessor
}

func (p *MLVideoProcessor) ProcessVideo(
    ctx context.Context,
    frame []byte,
    width, height uint32,
    format agent.VideoFormat,
) ([]byte, error) {
    // Preprocess frame for model
    input, err := p.preprocessor.Prepare(frame, width, height, format)
    if err != nil {
        return nil, err
    }
    
    // Run inference
    outputs, err := p.model.Run([]onnxruntime.Value{input})
    if err != nil {
        return nil, err
    }
    
    // Postprocess results
    processed, err := p.postprocessor.Process(outputs[0], width, height, format)
    if err != nil {
        return nil, err
    }
    
    return processed, nil
}
```

## Quality Control

### Video Quality Controller

```go
// Create quality controller
qc := agent.NewQualityController()

// Set custom adaptation policy
policy := agent.QualityAdaptationPolicy{
    LossThresholdUp:       0.02,  // 2% loss triggers quality decrease
    LossThresholdDown:     0.01,  // <1% loss allows quality increase
    BitrateThresholdUp:    0.8,   // 80% bitrate usage allows increase
    BitrateThresholdDown:  0.95,  // 95% bitrate usage triggers decrease
    RTTThresholdHigh:      200,   // 200ms RTT triggers decrease
    RTTThresholdLow:       100,   // <100ms RTT allows increase
    StableWindowUp:        10 * time.Second,
    StableWindowDown:      2 * time.Second,
    MinTimeBetweenChanges: 3 * time.Second,
    PreferTemporalScaling: true,
    AllowDynamicFPS:      true,
    MaxQuality:           livekit.VideoQuality_HIGH,
    MinQuality:           livekit.VideoQuality_LOW,
}
qc.SetAdaptationPolicy(policy)

// Monitor tracks using polling pattern
go func() {
    ticker := time.NewTicker(500 * time.Millisecond)
    defer ticker.Stop()
    
    knownTracks := make(map[string]bool)
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            for _, p := range room.GetRemoteParticipants() {
                for _, pub := range p.TrackPublications() {
                    trackID := pub.TrackPublication().SID()
                    if !knownTracks[trackID] && pub.TrackPublication().Kind() == livekit.TrackKind_VIDEO {
                        knownTracks[trackID] = true
                        
                        if track := pub.TrackPublication().Track(); track != nil {
                            subscription := &agent.PublisherTrackSubscription{
                                Track:          track.(*webrtc.TrackRemote),
                                Publication:    pub.TrackPublication(),
                                Participant:    p,
                                CurrentQuality: livekit.VideoQuality_HIGH,
                            }
                            qc.StartMonitoring(track.(*webrtc.TrackRemote), subscription)
                        }
                    }
                }
            }
        }
    }
}()
```

### Custom Quality Adaptation

```go
type AdaptiveQualityHandler struct {
    qc              *agent.QualityController
    networkMonitor  *NetworkMonitor
    cpuMonitor      *CPUMonitor
}

func (h *AdaptiveQualityHandler) OnJobAssigned(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    // Monitor system resources
    go h.monitorResources(ctx)
    
    // Monitor for tracks and quality changes using polling
    go h.monitorTracksAndQuality(ctx, room)
    
    <-ctx.Done()
    return nil
}

func (h *AdaptiveQualityHandler) monitorTracksAndQuality(ctx context.Context, room *lksdk.Room) {
    ticker := time.NewTicker(500 * time.Millisecond)
    defer ticker.Stop()
    
    knownTracks := make(map[string]bool)
    lastQualityUpdate := make(map[string]livekit.ConnectionQuality)
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            // Check for new tracks
            for _, p := range room.GetRemoteParticipants() {
                for _, pub := range p.TrackPublications() {
                    trackID := pub.TrackPublication().SID()
                    if !knownTracks[trackID] {
                        knownTracks[trackID] = true
                        
                        if pub.TrackPublication().Kind() == livekit.TrackKind_VIDEO {
                            if track := pub.TrackPublication().Track(); track != nil {
                                h.setupAdaptiveQuality(track.(*webrtc.TrackRemote), pub.TrackPublication(), p)
                            }
                        }
                    }
                }
                
                // Check connection quality changes
                if p.ConnectionQuality() != lastQualityUpdate[p.SID()] {
                    lastQualityUpdate[p.SID()] = p.ConnectionQuality()
                    h.handleQualityUpdate(&livekit.ConnectionQualityInfo{
                        ParticipantSid: p.SID(),
                        Quality:        p.ConnectionQuality(),
                    })
                }
            }
        }
    }
}

func (h *AdaptiveQualityHandler) setupAdaptiveQuality(
    track *webrtc.TrackRemote,
    pub *lksdk.RemoteTrackPublication,
    participant *lksdk.RemoteParticipant,
) {
    subscription := &agent.PublisherTrackSubscription{
        Track:       track,
        Publication: pub,
        Participant: participant,
    }
    
    // Calculate initial quality based on conditions
    networkQuality := h.networkMonitor.GetQuality()
    cpuLoad := h.cpuMonitor.GetLoad()
    
    initialQuality := h.qc.CalculateOptimalQuality(networkQuality, subscription)
    
    // Adjust for CPU load
    if cpuLoad > 0.8 && initialQuality == livekit.VideoQuality_HIGH {
        initialQuality = livekit.VideoQuality_MEDIUM
    }
    
    // Apply initial quality
    h.qc.ApplyQualitySettings(track, initialQuality)
    
    // Start monitoring
    h.qc.StartMonitoring(track, subscription)
}

func (h *AdaptiveQualityHandler) monitorResources(ctx context.Context) {
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            cpuLoad := h.cpuMonitor.GetLoad()
            
            // Disable adaptation if CPU is overloaded
            if cpuLoad > 0.9 {
                for _, trackID := range h.getMonitoredTracks() {
                    h.qc.EnableAdaptation(trackID, false)
                    h.qc.ApplyQualitySettings(trackID, livekit.VideoQuality_LOW)
                }
            } else if cpuLoad < 0.7 {
                // Re-enable adaptation when CPU recovers
                for _, trackID := range h.getMonitoredTracks() {
                    h.qc.EnableAdaptation(trackID, true)
                }
            }
        }
    }
}
```

## Track Subscription Management

### Advanced Subscription Manager

```go
type SubscriptionManager struct {
    maxSubscriptions    int
    priorityCalculator  SubscriptionPriorityCalculator
    subscriptions       map[string]*ManagedSubscription
    mu                  sync.RWMutex
}

type ManagedSubscription struct {
    Track        *webrtc.TrackRemote
    Publication  *lksdk.RemoteTrackPublication
    Participant  *lksdk.RemoteParticipant
    Priority     int
    Quality      livekit.VideoQuality
    LastActivity time.Time
    Metrics      *SubscriptionMetrics
}

func (sm *SubscriptionManager) ManageSubscriptions(ctx context.Context, room *lksdk.Room) {
    // Monitor tracks using polling pattern
    go sm.monitorTracks(ctx, room)
    
    // Periodic optimization
    go sm.optimizeSubscriptions(ctx, room)
}

func (sm *SubscriptionManager) monitorTracks(ctx context.Context, room *lksdk.Room) {
    ticker := time.NewTicker(500 * time.Millisecond)
    defer ticker.Stop()
    
    knownTracks := make(map[string]bool)
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            currentTracks := make(map[string]bool)
            
            // Check for new tracks
            for _, p := range room.GetRemoteParticipants() {
                for _, pub := range p.TrackPublications() {
                    trackID := pub.TrackPublication().SID()
                    currentTracks[trackID] = true
                    
                    if !knownTracks[trackID] {
                        knownTracks[trackID] = true
                        sm.handleNewTrack(pub.TrackPublication(), p)
                    }
                }
            }
            
            // Check for removed tracks
            for trackID := range knownTracks {
                if !currentTracks[trackID] {
                    delete(knownTracks, trackID)
                    sm.handleTrackRemoved(trackID)
                }
            }
        }
    }
}

func (sm *SubscriptionManager) handleNewTrack(pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
    priority := sm.priorityCalculator.Calculate(pub, rp)
    
    sm.mu.Lock()
    defer sm.mu.Unlock()
    
    // Check if we can subscribe
    if len(sm.subscriptions) >= sm.maxSubscriptions {
        // Find lowest priority subscription
        lowestPriority := priority
        var lowestID string
        
        for id, sub := range sm.subscriptions {
            if sub.Priority < lowestPriority {
                lowestPriority = sub.Priority
                lowestID = id
            }
        }
        
        // Replace if new track has higher priority
        if lowestID != "" && priority > lowestPriority {
            sm.unsubscribe(lowestID)
        } else {
            // Can't subscribe to new track
            return
        }
    }
    
    // Subscribe to track
    if err := pub.SetSubscribed(true); err != nil {
        log.Printf("Failed to subscribe: %v", err)
        return
    }
    
    // Add to managed subscriptions
    sm.subscriptions[pub.SID()] = &ManagedSubscription{
        Publication:  pub,
        Participant:  rp,
        Priority:     priority,
        Quality:      livekit.VideoQuality_HIGH,
        LastActivity: time.Now(),
        Metrics:      NewSubscriptionMetrics(),
    }
}

func (sm *SubscriptionManager) optimizeSubscriptions(ctx context.Context, room *lksdk.Room) {
    ticker := time.NewTicker(10 * time.Second)
    defer ticker.Stop()
    
    for range ticker.C {
        sm.mu.Lock()
        
        // Update metrics and priorities
        for _, sub := range sm.subscriptions {
            sub.Metrics.Update()
            sub.Priority = sm.priorityCalculator.Recalculate(sub)
        }
        
        // Sort by priority
        sorted := sm.getSortedSubscriptions()
        
        // Adjust quality based on position
        for i, sub := range sorted {
            targetQuality := sm.calculateQualityForPosition(i, len(sorted))
            
            if sub.Quality != targetQuality {
                sm.updateSubscriptionQuality(sub, targetQuality)
            }
        }
        
        sm.mu.Unlock()
    }
}
```

### Priority-Based Subscription

```go
type SubscriptionPriorityCalculator interface {
    Calculate(pub *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) int
    Recalculate(sub *ManagedSubscription) int
}

type SmartPriorityCalculator struct {
    roomMode string // "presentation", "discussion", "webinar"
}

func (c *SmartPriorityCalculator) Calculate(pub *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) int {
    priority := 50 // Base priority
    
    // Track type
    if pub.Source() == livekit.TrackSource_SCREEN_SHARE {
        priority += 40 // Screen share is high priority
    } else if pub.Source() == livekit.TrackSource_CAMERA {
        priority += 20
    }
    
    // Participant role (from metadata)
    var metadata map[string]interface{}
    if err := json.Unmarshal([]byte(participant.Metadata()), &metadata); err == nil {
        if role, ok := metadata["role"].(string); ok {
            switch role {
            case "presenter":
                priority += 30
            case "moderator":
                priority += 20
            case "participant":
                priority += 10
            }
        }
    }
    
    // Room mode adjustments
    switch c.roomMode {
    case "presentation":
        if pub.Source() == livekit.TrackSource_SCREEN_SHARE {
            priority += 20 // Extra boost for screen share
        }
    case "discussion":
        // More balanced priorities
        priority += 10
    case "webinar":
        if isSpeaker(participant) {
            priority += 40
        }
    }
    
    return priority
}

func (c *SmartPriorityCalculator) Recalculate(sub *ManagedSubscription) int {
    basePriority := c.Calculate(sub.Publication, sub.Participant)
    
    // Adjust based on activity
    timeSinceActivity := time.Since(sub.LastActivity)
    if timeSinceActivity < 10*time.Second {
        basePriority += 15 // Recently active
    } else if timeSinceActivity > 5*time.Minute {
        basePriority -= 10 // Inactive
    }
    
    // Adjust based on metrics
    if sub.Metrics.PacketLossRate > 0.05 {
        basePriority -= 5 // Poor connection
    }
    
    if sub.Metrics.ViewerCount > 0 {
        basePriority += min(sub.Metrics.ViewerCount*5, 20) // Popular track
    }
    
    return basePriority
}
```

## Real-time Processing

### Audio Effects Processing

```go
type AudioEffectsProcessor struct {
    effects []AudioEffect
    buffer  *AudioRingBuffer
}

type AudioEffect interface {
    Process(samples []float32, sampleRate uint32) []float32
    GetLatency() time.Duration
}

// Echo effect
type EchoEffect struct {
    delay     time.Duration
    decay     float32
    mix       float32
    buffer    []float32
    position  int
}

func (e *EchoEffect) Process(samples []float32, sampleRate uint32) []float32 {
    delaySamples := int(float64(sampleRate) * e.delay.Seconds())
    
    if len(e.buffer) != delaySamples {
        e.buffer = make([]float32, delaySamples)
        e.position = 0
    }
    
    output := make([]float32, len(samples))
    
    for i, sample := range samples {
        // Get delayed sample
        delayed := e.buffer[e.position]
        
        // Store current sample with decay
        e.buffer[e.position] = sample + delayed*e.decay
        
        // Mix original and delayed
        output[i] = sample*(1-e.mix) + delayed*e.mix
        
        // Advance position
        e.position = (e.position + 1) % len(e.buffer)
    }
    
    return output
}

// Pitch shift effect
type PitchShiftEffect struct {
    semitones   float32
    pitchShifter *PitchShifter
}

func (p *PitchShiftEffect) Process(samples []float32, sampleRate uint32) []float32 {
    return p.pitchShifter.Process(samples, p.semitones, sampleRate)
}
```

### Video Filter Pipeline

```go
type VideoFilterPipeline struct {
    filters []VideoFilter
}

type VideoFilter interface {
    Apply(frame image.Image) image.Image
    GetName() string
}

// Blur filter
type BlurFilter struct {
    radius float64
}

func (f *BlurFilter) Apply(frame image.Image) image.Image {
    bounds := frame.Bounds()
    output := image.NewRGBA(bounds)
    
    // Apply Gaussian blur
    kernel := generateGaussianKernel(f.radius)
    
    for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
        for x := bounds.Min.X; x < bounds.Max.X; x++ {
            output.Set(x, y, f.convolve(frame, x, y, kernel))
        }
    }
    
    return output
}

// Background replacement filter
type BackgroundReplacementFilter struct {
    segmentationModel *onnxruntime.Session
    backgroundImage   image.Image
}

func (f *BackgroundReplacementFilter) Apply(frame image.Image) image.Image {
    // Run segmentation
    mask := f.runSegmentation(frame)
    
    // Composite with background
    bounds := frame.Bounds()
    output := image.NewRGBA(bounds)
    
    for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
        for x := bounds.Min.X; x < bounds.Max.X; x++ {
            maskValue := mask.GrayAt(x, y).Y
            
            if maskValue > 128 { // Foreground
                output.Set(x, y, frame.At(x, y))
            } else { // Background
                output.Set(x, y, f.backgroundImage.At(x, y))
            }
        }
    }
    
    return output
}
```

## Media Synchronization

### Audio-Video Sync

```go
type AVSyncManager struct {
    audioBuffer      *TimestampedBuffer
    videoBuffer      *TimestampedBuffer
    syncThreshold    time.Duration
    maxBufferTime    time.Duration
}

func (s *AVSyncManager) ProcessMediaStreams(
    audioTrack *webrtc.TrackRemote,
    videoTrack *webrtc.TrackRemote,
    output MediaOutput,
) {
    // Audio processing goroutine
    go func() {
        for {
            sample, _, err := audioTrack.ReadSample()
            if err != nil {
                return
            }
            
            s.audioBuffer.Add(sample.Data, sample.Timestamp)
        }
    }()
    
    // Video processing goroutine
    go func() {
        for {
            sample, _, err := videoTrack.ReadSample()
            if err != nil {
                return
            }
            
            s.videoBuffer.Add(sample.Data, sample.Timestamp)
        }
    }()
    
    // Synchronization loop
    go s.syncLoop(output)
}

func (s *AVSyncManager) syncLoop(output MediaOutput) {
    ticker := time.NewTicker(20 * time.Millisecond) // 50 FPS
    defer ticker.Stop()
    
    for range ticker.C {
        // Get current timestamps
        audioTS := s.audioBuffer.CurrentTimestamp()
        videoTS := s.videoBuffer.CurrentTimestamp()
        
        if audioTS == 0 || videoTS == 0 {
            continue // Waiting for data
        }
        
        // Calculate sync offset
        offset := audioTS - videoTS
        
        if abs(offset) > s.syncThreshold {
            // Need to sync
            if offset > 0 {
                // Audio is ahead, skip audio samples
                s.audioBuffer.SkipTo(videoTS)
            } else {
                // Video is ahead, skip video frames
                s.videoBuffer.SkipTo(audioTS)
            }
        }
        
        // Output synchronized media
        if audioData := s.audioBuffer.GetNext(); audioData != nil {
            output.WriteAudio(audioData)
        }
        
        if videoData := s.videoBuffer.GetNext(); videoData != nil {
            output.WriteVideo(videoData)
        }
    }
}
```

## Performance Optimization

### Buffer Management

```go
type OptimizedMediaBuffer struct {
    pool         *sync.Pool
    maxSize      int
    currentSize  int
    mu           sync.RWMutex
    queue        chan *MediaPacket
}

func NewOptimizedMediaBuffer(maxSize int) *OptimizedMediaBuffer {
    return &OptimizedMediaBuffer{
        pool: &sync.Pool{
            New: func() interface{} {
                return &MediaPacket{
                    Data: make([]byte, 0, 4096), // Pre-allocate common size
                }
            },
        },
        maxSize: maxSize,
        queue:   make(chan *MediaPacket, maxSize),
    }
}

func (b *OptimizedMediaBuffer) Add(data []byte, timestamp uint32) error {
    packet := b.pool.Get().(*MediaPacket)
    packet.Data = packet.Data[:0] // Reset slice
    packet.Data = append(packet.Data, data...)
    packet.Timestamp = timestamp
    
    select {
    case b.queue <- packet:
        atomic.AddInt32(&b.currentSize, 1)
        return nil
    default:
        // Buffer full, return to pool
        b.pool.Put(packet)
        return errors.New("buffer full")
    }
}

func (b *OptimizedMediaBuffer) Get() *MediaPacket {
    select {
    case packet := <-b.queue:
        atomic.AddInt32(&b.currentSize, -1)
        return packet
    default:
        return nil
    }
}

func (b *OptimizedMediaBuffer) Release(packet *MediaPacket) {
    // Return to pool for reuse
    if cap(packet.Data) <= 65536 { // Don't pool very large buffers
        b.pool.Put(packet)
    }
}
```

### Parallel Processing

```go
type ParallelMediaProcessor struct {
    workers      int
    inputChan    chan MediaData
    outputChan   chan MediaData
    processorFunc func(MediaData) (MediaData, error)
}

func (p *ParallelMediaProcessor) Start(ctx context.Context) {
    // Start worker pool
    var wg sync.WaitGroup
    
    for i := 0; i < p.workers; i++ {
        wg.Add(1)
        go func(workerID int) {
            defer wg.Done()
            
            for {
                select {
                case <-ctx.Done():
                    return
                case data := <-p.inputChan:
                    processed, err := p.processorFunc(data)
                    if err != nil {
                        log.Printf("Worker %d processing error: %v", workerID, err)
                        continue
                    }
                    
                    select {
                    case p.outputChan <- processed:
                    case <-ctx.Done():
                        return
                    }
                }
            }
        }(i)
    }
    
    // Wait for all workers to finish
    go func() {
        wg.Wait()
        close(p.outputChan)
    }()
}
```

## Best Practices

1. **Pipeline Design**
   - Keep stages focused on single responsibilities
   - Order stages by computational complexity (lightest first)
   - Use appropriate buffer sizes between stages

2. **Quality Control**
   - Monitor network conditions continuously
   - Adapt quality proactively, not reactively
   - Consider both network and system resources

3. **Track Management**
   - Subscribe only to necessary tracks
   - Implement priority-based subscription
   - Clean up subscriptions promptly

4. **Performance**
   - Use object pools for frequent allocations
   - Implement parallel processing for CPU-intensive tasks
   - Monitor and limit memory usage

5. **Synchronization**
   - Maintain timestamp accuracy
   - Handle clock drift between sources
   - Implement appropriate buffering strategies

## Next Steps

- Explore complete [Examples](examples/README.md)
- See [API Reference](api-reference.md) for detailed documentation
- Check [Troubleshooting](troubleshooting.md) for common issues
- Review [Advanced Features](advanced-features.md) for more capabilities