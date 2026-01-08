/**
 * Call Replay Player
 *
 * Main controller for multi-participant call replay with video switching
 * and simultaneous audio mixing. Designed for iOS compatibility where
 * only one <video> and one <audio> element can be active.
 *
 * Architecture:
 * - Video: HLS.js for adaptive HLS playback, switch between participants
 * - Audio: WebCodecs + AudioWorklet for mixing all participants simultaneously
 *
 * Features:
 * - Multi-participant video switching
 * - Simultaneous audio mixing via AudioWorklet
 * - Cross-participant time synchronization
 * - Playback controls (play, pause, seek)
 * - iOS Safari compatibility
 */

import { AudioFetcher } from './audio-fetcher.js';
import { OpusDecoder } from './opus-decoder.js';

export class CallReplayPlayer extends EventTarget {
  /**
   * @param {Object} options - Player options
   * @param {HTMLVideoElement} options.videoElement - Video element for HLS playback
   * @param {number} options.maxParticipants - Maximum participants (default: 10)
   */
  constructor(options = {}) {
    super();

    if (!options.videoElement) {
      throw new Error('videoElement is required');
    }

    this.videoElement = options.videoElement;
    this.maxParticipants = options.maxParticipants ?? 10;

    /** @type {Map<string, ParticipantPlayer>} */
    this.participants = new Map();

    /** @type {string|null} */
    this.activeVideoParticipant = null;

    /** @type {AudioContext|null} */
    this.audioContext = null;

    /** @type {AudioWorkletNode|null} */
    this.mixerNode = null;

    /** @type {boolean} */
    this.isPlaying = false;

    /** @type {number} */
    this.currentTime = 0;

    /** @type {number} */
    this.duration = 0;

    /** @type {Object|null} */
    this.hls = null;

    this._mixerReady = false;
    this._boundTimeUpdate = this._onVideoTimeUpdate.bind(this);
    this._boundVideoWaiting = this._onVideoWaiting.bind(this);
    this._boundVideoPlaying = this._onVideoPlaying.bind(this);
    this._boundVideoEnded = this._onVideoEnded.bind(this);
  }

  /**
   * Initialize the player
   */
  async init() {
    // Create AudioContext
    this.audioContext = new (window.AudioContext || window.webkitAudioContext)({
      sampleRate: 48000
    });

    // Load and register AudioWorklet
    await this.audioContext.audioWorklet.addModule('mixer-worklet.js');

    // Create mixer node
    this.mixerNode = new AudioWorkletNode(this.audioContext, 'audio-mixer', {
      numberOfInputs: 0,
      numberOfOutputs: 1,
      outputChannelCount: [2]
    });

    // Connect to destination
    this.mixerNode.connect(this.audioContext.destination);

    // Handle messages from worklet
    this.mixerNode.port.onmessage = (event) => {
      if (event.data.type === 'ready') {
        this._mixerReady = true;
        this.dispatchEvent(new CustomEvent('mixerReady'));
      } else if (event.data.type === 'timing') {
        // Log timing info for debugging sync issues
        const videoTime = this.videoElement ? this.videoElement.currentTime : 0;
        const audioTime = event.data.audioTime;
        const drift = audioTime - videoTime;
        console.log(`[MixerTiming] audioTime=${audioTime.toFixed(3)}s, videoTime=${videoTime.toFixed(3)}s, drift=${drift.toFixed(3)}s`);
      }
    };

    // Setup video element events
    this.videoElement.addEventListener('timeupdate', this._boundTimeUpdate);
    this.videoElement.addEventListener('waiting', this._boundVideoWaiting);
    this.videoElement.addEventListener('playing', this._boundVideoPlaying);
    this.videoElement.addEventListener('ended', this._boundVideoEnded);

    this.dispatchEvent(new CustomEvent('initialized'));
  }

  /**
   * Add a participant to the player
   * @param {string} participantId - Unique participant identifier
   * @param {string} baseUrl - Base URL for participant's recordings
   * @param {Object} options - Participant options
   * @param {string} options.displayName - Display name for participant
   * @returns {Promise<ParticipantPlayer>}
   */
  async addParticipant(participantId, baseUrl, options = {}) {
    if (this.participants.size >= this.maxParticipants) {
      throw new Error(`Maximum participants (${this.maxParticipants}) reached`);
    }

    if (this.participants.has(participantId)) {
      throw new Error(`Participant ${participantId} already added`);
    }

    const participant = new ParticipantPlayer({
      id: participantId,
      baseUrl,
      displayName: options.displayName || participantId,
      audioContext: this.audioContext,
      mixerNode: this.mixerNode
    });

    await participant.init();

    // Set video element reference for A/V sync
    participant.setVideoElement(this.videoElement);

    this.participants.set(participantId, participant);

    // Update total duration
    const participantDuration = participant.getDuration();
    if (participantDuration > this.duration) {
      this.duration = participantDuration;
    }

    // Notify mixer of new participant
    this.mixerNode.port.postMessage({
      type: 'addParticipant',
      participantId,
      volume: 1.0
    });

    // If this is the first participant, make it active for video
    if (this.participants.size === 1) {
      await this.switchVideo(participantId);
    }

    this.dispatchEvent(new CustomEvent('participantAdded', {
      detail: { participantId, displayName: options.displayName }
    }));

    return participant;
  }

  /**
   * Remove a participant from the player
   * @param {string} participantId
   */
  removeParticipant(participantId) {
    const participant = this.participants.get(participantId);
    if (!participant) return;

    // Stop and cleanup participant
    participant.destroy();
    this.participants.delete(participantId);

    // Notify mixer
    this.mixerNode.port.postMessage({
      type: 'removeParticipant',
      participantId
    });

    // If this was the active video participant, switch to another
    if (this.activeVideoParticipant === participantId) {
      const nextParticipant = this.participants.keys().next().value;
      if (nextParticipant) {
        this.switchVideo(nextParticipant);
      } else {
        this.activeVideoParticipant = null;
      }
    }

    this.dispatchEvent(new CustomEvent('participantRemoved', {
      detail: { participantId }
    }));
  }

  /**
   * Switch video to a different participant
   * @param {string} participantId
   */
  async switchVideo(participantId) {
    const participant = this.participants.get(participantId);
    if (!participant) {
      throw new Error(`Participant ${participantId} not found`);
    }

    // Detach current HLS instance
    if (this.hls) {
      this.hls.destroy();
      this.hls = null;
    }

    // Load new participant's video
    const videoUrl = participant.getVideoUrl();

    if (window.Hls && window.Hls.isSupported()) {
      // Use HLS.js
      this.hls = new window.Hls({
        enableWorker: true,
        lowLatencyMode: false
      });

      this.hls.loadSource(videoUrl);
      this.hls.attachMedia(this.videoElement);

      await new Promise((resolve, reject) => {
        this.hls.once(window.Hls.Events.MANIFEST_PARSED, resolve);
        this.hls.once(window.Hls.Events.ERROR, (event, data) => {
          if (data.fatal) reject(new Error(data.error));
        });
      });
    } else if (this.videoElement.canPlayType('application/vnd.apple.mpegurl')) {
      // Native HLS support (Safari)
      this.videoElement.src = videoUrl;
      await new Promise((resolve) => {
        this.videoElement.addEventListener('loadedmetadata', resolve, { once: true });
      });
    } else {
      throw new Error('HLS not supported');
    }

    // Sync video position
    if (this.currentTime > 0) {
      this.videoElement.currentTime = this.currentTime;
    }

    // Resume playback if was playing
    if (this.isPlaying) {
      await this.videoElement.play();
    }

    this.activeVideoParticipant = participantId;

    this.dispatchEvent(new CustomEvent('videoSwitched', {
      detail: { participantId }
    }));
  }

  /**
   * Start playback
   */
  async play() {
    if (this.isPlaying) return;

    // Resume AudioContext if suspended
    if (this.audioContext.state === 'suspended') {
      await this.audioContext.resume();
    }

    // Clear all audio buffers to ensure fresh start
    this.mixerNode.port.postMessage({ type: 'clearAll' });

    // Wait for video to actually start playing before starting audio
    // This ensures audio doesn't run ahead during video buffering
    const waitForVideoPlaying = new Promise((resolve) => {
      const onPlaying = () => {
        this.videoElement.removeEventListener('playing', onPlaying);
        resolve();
      };
      this.videoElement.addEventListener('playing', onPlaying);
    });

    // Start video playback
    if (this.activeVideoParticipant) {
      const playPromise = this.videoElement.play();
      await waitForVideoPlaying;
      await playPromise;
    }

    // Now that video is actually playing, start audio at the video's current position
    const videoTime = this.videoElement.currentTime;
    console.log(`[PlayStart] Video started at currentTime=${videoTime.toFixed(3)}s`);

    // Start audio decoding for all participants (this begins filling the buffer)
    for (const [id, participant] of this.participants) {
      // Log first segment info for debugging
      if (participant.audioFetcher && participant.audioFetcher.manifest) {
        const firstSeg = participant.audioFetcher.manifest.segments[0];
        if (firstSeg) {
          console.log(`[PlayStart] Participant ${id}: first segment startTime=${firstSeg.startTime.toFixed(3)}s, duration=${firstSeg.duration.toFixed(3)}s`);
        }
      }
      participant.startAudio(videoTime);
    }

    // Wait a short moment for audio buffer to fill before starting playback
    // This prevents underrun at the start
    await new Promise(resolve => setTimeout(resolve, 150));

    const videoTimeAfterWait = this.videoElement.currentTime;
    console.log(`[PlayStart] After 150ms wait: videoTime=${videoTimeAfterWait.toFixed(3)}s (advanced ${(videoTimeAfterWait - videoTime).toFixed(3)}s)`);

    // Tell mixer to start outputting (now that buffer has some data)
    this.mixerNode.port.postMessage({ type: 'play' });

    this.isPlaying = true;
    this.dispatchEvent(new CustomEvent('play'));
  }

  /**
   * Pause playback
   */
  pause() {
    if (!this.isPlaying) return;

    // Pause video
    this.videoElement.pause();

    // Tell mixer to stop outputting (prevents audio from continuing)
    this.mixerNode.port.postMessage({ type: 'pause' });

    // Pause audio decoding for all participants
    for (const [id, participant] of this.participants) {
      participant.stopAudio();
    }

    this.isPlaying = false;
    this.dispatchEvent(new CustomEvent('pause'));
  }

  /**
   * Seek to a specific time
   * @param {number} timeSeconds - Time in seconds
   */
  async seek(timeSeconds) {
    this.currentTime = Math.max(0, Math.min(timeSeconds, this.duration));

    // Tell mixer to pause and clear buffers during seek
    this.mixerNode.port.postMessage({ type: 'pause' });
    this.mixerNode.port.postMessage({ type: 'clearAll' });

    // Seek video
    this.videoElement.currentTime = this.currentTime;

    // Wait for the video element to settle on the actual seek position before syncing audio.
    await new Promise((resolve) => {
      const onSeeked = () => {
        this.videoElement.removeEventListener('seeked', onSeeked);
        resolve();
      };
      this.videoElement.addEventListener('seeked', onSeeked);
    });

    const actualTime = this.videoElement.currentTime;
    this.currentTime = actualTime;

    // Seek audio for all participants
    for (const [id, participant] of this.participants) {
      await participant.seekAudio(actualTime);
    }

    // If was playing, resume
    if (this.isPlaying) {
      this.mixerNode.port.postMessage({ type: 'play' });
    }

    this.dispatchEvent(new CustomEvent('seeked', {
      detail: { time: actualTime }
    }));
  }

  /**
   * Set volume for a specific participant
   * @param {string} participantId
   * @param {number} volume - Volume 0-2 (1 = normal)
   */
  setParticipantVolume(participantId, volume) {
    this.mixerNode.port.postMessage({
      type: 'setVolume',
      participantId,
      volume: Math.max(0, Math.min(2, volume))
    });
  }

  /**
   * Get list of participant IDs
   * @returns {string[]}
   */
  getParticipantIds() {
    return Array.from(this.participants.keys());
  }

  /**
   * Get participant info
   * @param {string} participantId
   * @returns {Object|null}
   */
  getParticipantInfo(participantId) {
    const participant = this.participants.get(participantId);
    if (!participant) return null;

    return {
      id: participantId,
      displayName: participant.displayName,
      duration: participant.getDuration(),
      hasVideo: true,
      hasAudio: true
    };
  }

  /**
   * Handle video time update
   * @private
   */
  _onVideoTimeUpdate() {
    this.currentTime = this.videoElement.currentTime;
    this.dispatchEvent(new CustomEvent('timeupdate', {
      detail: { time: this.currentTime, duration: this.duration }
    }));
  }

  /**
   * Handle video waiting/buffering - pause audio to prevent desync
   * @private
   */
  _onVideoWaiting() {
    if (this.isPlaying) {
      // Temporarily pause audio mixer while video is buffering
      this.mixerNode.port.postMessage({ type: 'pause' });
      console.log('Video buffering - audio paused');
    }
  }

  /**
   * Handle video resumed playing after buffer - resume audio
   * @private
   */
  _onVideoPlaying() {
    if (this.isPlaying) {
      // Resume audio mixer now that video is playing again
      this.mixerNode.port.postMessage({ type: 'play' });
      console.log('Video resumed - audio resumed');
    }
  }

  /**
   * Handle video ended - stop audio playback
   * @private
   */
  _onVideoEnded() {
    console.log('Video ended - stopping audio');
    this.pause();
    this.dispatchEvent(new CustomEvent('ended'));
  }

  /**
   * Clean up resources
   */
  destroy() {
    this.pause();

    // Remove video listeners
    this.videoElement.removeEventListener('timeupdate', this._boundTimeUpdate);
    this.videoElement.removeEventListener('waiting', this._boundVideoWaiting);
    this.videoElement.removeEventListener('playing', this._boundVideoPlaying);
    this.videoElement.removeEventListener('ended', this._boundVideoEnded);

    // Destroy HLS
    if (this.hls) {
      this.hls.destroy();
      this.hls = null;
    }

    // Destroy all participants
    for (const [id, participant] of this.participants) {
      participant.destroy();
    }
    this.participants.clear();

    // Clear mixer
    if (this.mixerNode) {
      this.mixerNode.port.postMessage({ type: 'clear' });
      this.mixerNode.disconnect();
      this.mixerNode = null;
    }

    // Close AudioContext
    if (this.audioContext) {
      this.audioContext.close();
      this.audioContext = null;
    }
  }
}

/**
 * Individual participant player
 * Handles video URL and audio decoding/mixing for one participant
 */
class ParticipantPlayer extends EventTarget {
  /**
   * @param {Object} options
   * @param {string} options.id - Participant ID
   * @param {string} options.baseUrl - Base URL for recordings
   * @param {string} options.displayName - Display name
   * @param {AudioContext} options.audioContext - Shared AudioContext
   * @param {AudioWorkletNode} options.mixerNode - Mixer node
   */
  constructor(options) {
    super();

    this.id = options.id;
    // Normalize baseUrl: strip trailing slash and any filename (e.g., video.m3u8)
    let baseUrl = options.baseUrl.endsWith('/') ? options.baseUrl.slice(0, -1) : options.baseUrl;
    // If URL ends with a filename, strip it to get the directory
    if (baseUrl.match(/\.(m3u8|json|ts|m4s|mp4)$/i)) {
      baseUrl = baseUrl.substring(0, baseUrl.lastIndexOf('/'));
    }
    this.baseUrl = baseUrl;
    this.displayName = options.displayName;
    this.audioContext = options.audioContext;
    this.mixerNode = options.mixerNode;

    /** @type {AudioFetcher|null} */
    this.audioFetcher = null;

    /** @type {OpusDecoder|null} */
    this.decoder = null;

    /** @type {number|null} */
    this.audioPlaybackTimer = null;

    /** @type {number} */
    this.currentSegmentIndex = 0;

    /** @type {number} */
    this.lastDecodedSegmentIndex = -1;

    /** @type {number} */
    this.pendingSkipSamples = 0;

    /** @type {boolean} */
    this.audioPlaying = false;

    /** @type {HTMLVideoElement|null} */
    this.videoElement = null;
  }

  /**
   * Initialize participant player
   */
  async init() {
    // Initialize audio fetcher
    this.audioFetcher = new AudioFetcher(this.baseUrl);
    await this.audioFetcher.init();

    // Initialize decoder
    this.decoder = new OpusDecoder({
      sampleRate: this.audioFetcher.manifest.sampleRate,
      channels: this.audioFetcher.manifest.channels
    });

    // Setup decoder output handler
    this.decoder.addEventListener('samples', (event) => {
      this._sendSamplesToMixer(event.detail);
    });

    await this.decoder.init(this.audioFetcher.initSegment);
  }

  /**
   * Get video playlist URL
   * @returns {string}
   */
  getVideoUrl() {
    return `${this.baseUrl}/video.m3u8`;
  }

  /**
   * Get total duration
   * @returns {number}
   */
  getDuration() {
    return this.audioFetcher ? this.audioFetcher.getTotalDuration() : 0;
  }

  /**
   * Set video element reference for A/V sync
   * @param {HTMLVideoElement} videoElement
   */
  setVideoElement(videoElement) {
    this.videoElement = videoElement;
  }

  /**
   * Start audio playback from a specific time
   * @param {number} startTime - Start time in seconds
   */
  async startAudio(startTime) {
    if (this.audioPlaying) return;

    this.audioPlaying = true;
    this.lastDecodedSegmentIndex = -1;
    this.pendingSkipSamples = 0;

    // Find starting segment
    const segInfo = this.audioFetcher.getSegmentByTime(startTime);
    if (segInfo) {
      this.currentSegmentIndex = segInfo.index;
      this.pendingSkipSamples = Math.floor(segInfo.offsetInSegment * this.audioFetcher.manifest.sampleRate);
    } else {
      this.currentSegmentIndex = 0;
    }

    // Start sync loop (runs frequently to stay in sync with video)
    this._syncAudioLoop();
  }

  /**
   * Stop audio playback
   */
  stopAudio() {
    this.audioPlaying = false;
    if (this.audioPlaybackTimer) {
      clearTimeout(this.audioPlaybackTimer);
      this.audioPlaybackTimer = null;
    }
  }

  /**
   * Seek audio to a specific time
   * @param {number} timeSeconds
   */
  async seekAudio(timeSeconds) {
    const wasPlaying = this.audioPlaying;

    // Stop current playback
    this.stopAudio();

    // Reset decoder
    await this.decoder.reset();

    // Find new segment
    const segInfo = this.audioFetcher.getSegmentByTime(timeSeconds);
    if (segInfo) {
      this.currentSegmentIndex = segInfo.index;
      this.pendingSkipSamples = Math.floor(segInfo.offsetInSegment * this.audioFetcher.manifest.sampleRate);
    } else {
      this.currentSegmentIndex = 0;
      this.pendingSkipSamples = 0;
    }
    this.lastDecodedSegmentIndex = -1;

    // Resume if was playing
    if (wasPlaying) {
      this.startAudio(timeSeconds);
    }
  }

  /**
   * Audio sync loop - syncs audio decoding with video currentTime
   * @private
   */
  async _syncAudioLoop() {
    if (!this.audioPlaying) return;

    const manifest = this.audioFetcher.manifest;

    // Get current video time for sync
    const videoTime = this.videoElement ? this.videoElement.currentTime : 0;

    // Find which segment should be playing based on video time
    const segInfo = this.audioFetcher.getSegmentByTime(videoTime);

    // Determine target segment index
    // If segInfo is null (video time before first segment), still decode from segment 0
    // to ensure we have audio ready when video reaches the first segment's start time
    let expectedSegment = 0;
    if (segInfo) {
      expectedSegment = segInfo.index;
    } else if (manifest.segments.length > 0) {
      // Video time is before first segment - prepare by prefetching from segment 0
      expectedSegment = 0;
    }

    const currentSegment = this.lastDecodedSegmentIndex;

    // If video is more than 2 segments ahead/behind, resync
    // Only resync if we've already decoded at least one segment
    if (currentSegment >= 0 && Math.abs(expectedSegment - currentSegment) > 2) {
      console.log(`Audio resync: video at segment ${expectedSegment}, audio at ${currentSegment}, videoTime=${videoTime.toFixed(2)}s`);
      // Clear mixer buffer for this participant and restart from correct position
      if (this.mixerNode) {
        this.mixerNode.port.postMessage({
          type: 'clearParticipant',
          participantId: this.id
        });
      }
      this.currentSegmentIndex = expectedSegment;
      this.lastDecodedSegmentIndex = expectedSegment - 1;
      if (segInfo) {
        this.pendingSkipSamples = Math.floor(segInfo.offsetInSegment * manifest.sampleRate);
      } else {
        this.pendingSkipSamples = 0;
      }
    }

    // Decode segments up to current video position + prefetch (2 segments ahead)
    const targetIndex = Math.min(expectedSegment + 2, manifest.segments.length - 1);

    while (this.currentSegmentIndex <= targetIndex &&
           this.currentSegmentIndex < manifest.segments.length) {

      // Skip if already decoded
      if (this.currentSegmentIndex <= this.lastDecodedSegmentIndex) {
        this.currentSegmentIndex++;
        continue;
      }

      try {
        const segmentData = await this.audioFetcher.getSegment(this.currentSegmentIndex);
        // Use the manifest's startTime for this segment as the base time
        // This ensures accurate timing even if GStreamer produces segment-local tfdt values
        const segmentInfo = manifest.segments[this.currentSegmentIndex];

        // Debug logging for timing analysis
        if (this.currentSegmentIndex <= 10 || this.currentSegmentIndex % 10 === 0) {
          console.log(`[AudioSync] Decoding segment ${this.currentSegmentIndex}: startTime=${segmentInfo.startTime.toFixed(3)}s, duration=${segmentInfo.duration.toFixed(3)}s, videoTime=${videoTime.toFixed(3)}s`);
        }

        await this.decoder.decode(segmentData, { baseTimeSeconds: segmentInfo.startTime });
        this.lastDecodedSegmentIndex = this.currentSegmentIndex;
        this.currentSegmentIndex++;
      } catch (error) {
        // Don't skip segments on transient fetch/decode errors (e.g., eventual consistency
        // while segments are still being uploaded). Skipping permanently shifts audio and
        // makes A/V sync unrecoverable until a full replay/seek.
        console.error(`Audio decode error for segment ${this.currentSegmentIndex} (will retry):`, error);
        break;
      }
    }

    // Check if reached end
    if (this.currentSegmentIndex >= manifest.segments.length) {
      this.audioPlaying = false;
      this.dispatchEvent(new CustomEvent('audioEnded'));
      return;
    }

    // Schedule next sync check (every ~100ms for responsive sync)
    this.audioPlaybackTimer = setTimeout(() => {
      requestAnimationFrame(() => this._syncAudioLoop());
    }, 100);
  }

  /**
   * Send decoded samples to mixer
   * @param {Object} detail - Sample details
   * @private
   */
  _sendSamplesToMixer(detail) {
    let { samples, channels, frames } = detail;

    if (this.pendingSkipSamples > 0 && frames > 0) {
      const skip = Math.min(this.pendingSkipSamples, frames);
      if (skip >= frames) {
        this.pendingSkipSamples -= skip;
        return;
      }

      this.pendingSkipSamples = 0;
      frames = frames - skip;
      samples = samples.map((ch) => ch.subarray(skip));
    }

    // Send each channel's samples to the mixer
    for (let ch = 0; ch < Math.min(channels, 2); ch++) {
      this.mixerNode.port.postMessage({
        type: 'audioData',
        participantId: this.id,
        samples: samples[ch],
        channel: ch
      });
    }
  }

  /**
   * Clean up resources
   */
  destroy() {
    this.stopAudio();

    if (this.decoder) {
      this.decoder.destroy();
      this.decoder = null;
    }

    if (this.audioFetcher) {
      this.audioFetcher.destroy();
      this.audioFetcher = null;
    }
  }
}
