/**
 * Audio Mixer Worklet Processor
 *
 * Mixes audio from multiple participants in real-time using AudioWorklet API.
 * Each participant's audio is received via MessagePort and mixed together.
 *
 * Features:
 * - Soft clipping using tanh() to prevent harsh distortion
 * - Per-participant volume control
 * - Dynamic participant add/remove
 * - Sample-accurate mixing for synchronization
 */

class AudioMixerProcessor extends AudioWorkletProcessor {
  constructor() {
    super();

    // Map of participant ID -> audio buffer ring
    this.participants = new Map();

    // Ring buffer configuration
    // Must be large enough to hold prefetched segments (3 segments at 2s each = 6s)
    // plus some margin for network variations and smooth playback
    this.bufferSize = 48000 * 10; // 10 seconds at 48kHz
    this.channels = 2;

    // Playback control
    this.isPlaying = false;

    // Timing tracking
    this.totalSamplesOutput = 0;
    this.lastReportTime = 0;

    // Handle messages from main thread
    this.port.onmessage = (event) => {
      this.handleMessage(event.data);
    };

    this.port.postMessage({ type: 'ready' });
  }

  handleMessage(data) {
    switch (data.type) {
      case 'addParticipant':
        this.addParticipant(data.participantId, data.volume || 1.0);
        break;

      case 'removeParticipant':
        this.removeParticipant(data.participantId);
        break;

      case 'audioData':
        this.receiveAudioData(data.participantId, data.samples, data.channel);
        break;

      case 'setVolume':
        this.setParticipantVolume(data.participantId, data.volume);
        break;

      case 'clear':
        this.participants.clear();
        break;

      case 'clearParticipant':
        this.clearParticipantBuffer(data.participantId);
        break;

      case 'play':
        this.isPlaying = true;
        break;

      case 'pause':
        this.isPlaying = false;
        break;

      case 'clearAll':
        // Clear all buffers on seek
        for (const [_, participant] of this.participants) {
          participant.buffers[0].fill(0);
          participant.buffers[1].fill(0);
          participant.writeIndices = [0, 0];
          participant.readIndex = 0;
        }
        // Reset timing counter
        this.totalSamplesOutput = 0;
        break;
    }
  }

  clearParticipantBuffer(participantId) {
    const participant = this.participants.get(participantId);
    if (participant) {
      // Reset buffers to silence
      participant.buffers[0].fill(0);
      participant.buffers[1].fill(0);
      participant.writeIndices = [0, 0];
      participant.readIndex = 0;
    }
  }

  addParticipant(participantId, volume = 1.0) {
    if (this.participants.has(participantId)) {
      return;
    }

    // Use separate writeIndex for each channel to handle samples arriving in separate messages
    this.participants.set(participantId, {
      buffers: [
        new Float32Array(this.bufferSize), // Left channel
        new Float32Array(this.bufferSize)  // Right channel
      ],
      writeIndices: [0, 0], // Separate write index per channel
      readIndex: 0,
      volume: volume,
      active: true
    });

    this.port.postMessage({
      type: 'participantAdded',
      participantId
    });
  }

  removeParticipant(participantId) {
    this.participants.delete(participantId);
    this.port.postMessage({
      type: 'participantRemoved',
      participantId
    });
  }

  setParticipantVolume(participantId, volume) {
    const participant = this.participants.get(participantId);
    if (participant) {
      participant.volume = Math.max(0, Math.min(2, volume)); // Clamp 0-2
    }
  }

  receiveAudioData(participantId, samples, channel) {
    const participant = this.participants.get(participantId);
    if (!participant) {
      return;
    }

    // Ensure channel index is valid
    if (channel < 0 || channel >= this.channels) {
      return;
    }

    const buffer = participant.buffers[channel];
    const len = samples.length;

    // Write samples to ring buffer using channel-specific write index
    let writeIdx = participant.writeIndices[channel];
    for (let i = 0; i < len; i++) {
      buffer[writeIdx] = samples[i];
      writeIdx = (writeIdx + 1) % this.bufferSize;
    }
    participant.writeIndices[channel] = writeIdx;
  }

  /**
   * Calculate available samples in ring buffer
   * Returns the minimum available across all channels to ensure synchronized playback
   */
  availableSamples(participant) {
    const read = participant.readIndex;
    let minAvailable = Infinity;

    for (let ch = 0; ch < this.channels; ch++) {
      const write = participant.writeIndices[ch];
      let available;
      if (write >= read) {
        available = write - read;
      } else {
        available = this.bufferSize - read + write;
      }
      minAvailable = Math.min(minAvailable, available);
    }

    return minAvailable === Infinity ? 0 : minAvailable;
  }

  /**
   * Main audio processing callback
   * Called by the audio system with output buffers to fill
   */
  process(inputs, outputs, parameters) {
    const output = outputs[0];
    if (!output || output.length === 0) {
      return true;
    }

    const blockSize = output[0].length;

    // Initialize output to silence
    for (let channel = 0; channel < output.length; channel++) {
      output[channel].fill(0);
    }

    // If not playing, just output silence
    if (!this.isPlaying) {
      return true;
    }

    // Mix all participants
    for (const [participantId, participant] of this.participants) {
      if (!participant.active) continue;

      const available = this.availableSamples(participant);
      if (available < blockSize) {
        // Not enough samples - skip this participant for this block
        // This can happen during initial buffering
        continue;
      }

      const volume = participant.volume;

      for (let channel = 0; channel < Math.min(output.length, this.channels); channel++) {
        const outChannel = output[channel];
        const buffer = participant.buffers[channel];

        for (let i = 0; i < blockSize; i++) {
          const sample = buffer[participant.readIndex] * volume;
          outChannel[i] += sample;
          participant.readIndex = (participant.readIndex + 1) % this.bufferSize;
        }

        // Reset read index for next channel (we read the same samples for both)
        participant.readIndex = (participant.readIndex - blockSize + this.bufferSize) % this.bufferSize;
      }

      // Advance read index once for all channels
      participant.readIndex = (participant.readIndex + blockSize) % this.bufferSize;
    }

    // Apply soft clipping to prevent harsh distortion when mixing many sources
    for (let channel = 0; channel < output.length; channel++) {
      const outChannel = output[channel];
      for (let i = 0; i < blockSize; i++) {
        // tanh provides smooth compression for values > 1
        outChannel[i] = Math.tanh(outChannel[i]);
      }
    }

    // Track samples output for timing analysis
    this.totalSamplesOutput += blockSize;

    // Report timing every ~1 second (48000 samples)
    const audioTime = this.totalSamplesOutput / 48000;
    if (audioTime - this.lastReportTime >= 1.0) {
      this.port.postMessage({
        type: 'timing',
        audioTime: audioTime,
        totalSamples: this.totalSamplesOutput
      });
      this.lastReportTime = audioTime;
    }

    return true; // Keep processor alive
  }
}

registerProcessor('audio-mixer', AudioMixerProcessor);
