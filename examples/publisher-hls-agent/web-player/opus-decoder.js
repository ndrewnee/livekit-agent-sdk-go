/**
 * Opus Decoder using WebCodecs API
 *
 * Decodes fMP4 segments containing Opus audio into raw PCM samples.
 * Uses the WebCodecs AudioDecoder API for hardware-accelerated decoding.
 *
 * Features:
 * - fMP4/CMAF demuxing to extract Opus frames
 * - WebCodecs AudioDecoder for Opus → PCM conversion
 * - Sample buffering for continuous playback
 * - Error recovery and codec reconfiguration
 */

export class OpusDecoder extends EventTarget {
  /**
   * @param {Object} options - Decoder options
   * @param {number} options.sampleRate - Expected sample rate (default: 48000)
   * @param {number} options.channels - Number of channels (default: 2)
   */
  constructor(options = {}) {
    super();

    this.sampleRate = options.sampleRate ?? 48000;
    this.channels = options.channels ?? 2;

    /** @type {AudioDecoder|null} */
    this.decoder = null;

    /** @type {Float32Array[]} */
    this.outputBuffers = [];

    /** @type {boolean} */
    this.initialized = false;

    /** @type {boolean} */
    this.flushing = false;

    /** @type {Uint8Array|null} */
    this.initData = null;

    // Opus codec description extracted from init segment
    /** @type {ArrayBuffer|null} */
    this.codecDescription = null;
  }

  /**
   * Initialize decoder with fMP4 init segment or first media segment
   * @param {ArrayBuffer|null} initSegment - fMP4 initialization segment (null if segments are self-contained)
   */
  async init(initSegment) {
    // Check WebCodecs support first
    if (!('AudioDecoder' in window)) {
      throw new Error('WebCodecs AudioDecoder not supported in this browser');
    }

    if (initSegment) {
      this.initData = new Uint8Array(initSegment);

      // Parse init segment to extract codec configuration
      const codecConfig = this.parseInitSegment(this.initData);

      // Check codec support
      const support = await AudioDecoder.isConfigSupported(codecConfig);
      if (!support.supported) {
        throw new Error('Opus codec configuration not supported');
      }

      // Create decoder
      this.decoder = new AudioDecoder({
        output: (audioData) => this.handleDecodedAudio(audioData),
        error: (error) => this.handleError(error)
      });

      this.decoder.configure(codecConfig);
    } else {
      // Segments are self-contained, will configure on first segment decode
      // Create decoder with default Opus config
      const defaultConfig = {
        codec: 'opus',
        sampleRate: this.sampleRate,
        numberOfChannels: this.channels
      };

      const support = await AudioDecoder.isConfigSupported(defaultConfig);
      if (!support.supported) {
        throw new Error('Opus codec configuration not supported');
      }

      this.decoder = new AudioDecoder({
        output: (audioData) => this.handleDecodedAudio(audioData),
        error: (error) => this.handleError(error)
      });

      this.decoder.configure(defaultConfig);
    }

    this.initialized = true;

    this.dispatchEvent(new CustomEvent('initialized', {
      detail: { sampleRate: this.sampleRate, channels: this.channels }
    }));
  }

  /**
   * Parse fMP4 init segment to extract codec configuration
   * @param {Uint8Array} data - Init segment data
   * @returns {AudioDecoderConfig}
   */
  parseInitSegment(data) {
    // Parse MP4 boxes to find codec configuration
    const boxes = this.parseMP4Boxes(data);

    // Find the moov box
    const moov = boxes.find(b => b.type === 'moov');
    if (!moov) {
      throw new Error('No moov box found in init segment');
    }

    // Parse moov children to find trak → mdia → minf → stbl → stsd → mp4a/Opus
    const moovChildren = this.parseMP4Boxes(moov.data);
    const trak = moovChildren.find(b => b.type === 'trak');
    if (!trak) {
      throw new Error('No trak box found');
    }

    // Navigate: trak → mdia → minf → stbl → stsd
    const trakChildren = this.parseMP4Boxes(trak.data);
    const mdia = trakChildren.find(b => b.type === 'mdia');
    if (!mdia) throw new Error('No mdia box found');

    const mdiaChildren = this.parseMP4Boxes(mdia.data);
    const minf = mdiaChildren.find(b => b.type === 'minf');
    if (!minf) throw new Error('No minf box found');

    const minfChildren = this.parseMP4Boxes(minf.data);
    const stbl = minfChildren.find(b => b.type === 'stbl');
    if (!stbl) throw new Error('No stbl box found');

    const stblChildren = this.parseMP4Boxes(stbl.data);
    const stsd = stblChildren.find(b => b.type === 'stsd');
    if (!stsd) throw new Error('No stsd box found');

    // Parse stsd to find Opus entry
    // stsd has 8 bytes of version/flags/entry_count before entries
    const stsdData = stsd.data;
    const entryCount = new DataView(stsdData.buffer, stsdData.byteOffset + 4, 4).getUint32(0);

    if (entryCount === 0) {
      throw new Error('No entries in stsd');
    }

    // Parse first entry (offset 8 in stsd data)
    const entryData = stsdData.slice(8);
    const entryBoxes = this.parseMP4Boxes(entryData);

    // Look for Opus box
    let opusBox = entryBoxes.find(b => b.type === 'Opus');
    if (!opusBox) {
      // May be wrapped in mp4a
      const mp4a = entryBoxes.find(b => b.type === 'mp4a');
      if (mp4a) {
        // mp4a has audio sample entry header (28 bytes) before children
        const mp4aChildren = this.parseMP4Boxes(mp4a.data.slice(28));
        opusBox = mp4aChildren.find(b => b.type === 'dOps');
      }
    }

    // Extract Opus specific config (dOps box)
    let dOps = null;
    if (opusBox && opusBox.type === 'Opus') {
      // Opus sample entry: 28 bytes header + children
      const opusChildren = this.parseMP4Boxes(opusBox.data.slice(28));
      dOps = opusChildren.find(b => b.type === 'dOps');
    } else if (opusBox && opusBox.type === 'dOps') {
      dOps = opusBox;
    }

    // Build codec config
    const config = {
      codec: 'opus',
      sampleRate: this.sampleRate,
      numberOfChannels: this.channels
    };

    // If we found dOps, include it as description
    if (dOps) {
      // dOps box content is the OpusSpecificBox
      this.codecDescription = dOps.data.buffer.slice(
        dOps.data.byteOffset,
        dOps.data.byteOffset + dOps.data.byteLength
      );
      config.description = this.codecDescription;
    }

    return config;
  }

  /**
   * Parse MP4 boxes from data
   * @param {Uint8Array} data
   * @returns {Array<{type: string, data: Uint8Array, size: number}>}
   */
  parseMP4Boxes(data) {
    const boxes = [];
    let offset = 0;

    while (offset + 8 <= data.length) {
      const view = new DataView(data.buffer, data.byteOffset + offset);
      let size = view.getUint32(0);
      const type = String.fromCharCode(
        data[offset + 4],
        data[offset + 5],
        data[offset + 6],
        data[offset + 7]
      );

      let headerSize = 8;
      if (size === 1) {
        // 64-bit size
        if (offset + 16 > data.length) break;
        size = Number(view.getBigUint64(8));
        headerSize = 16;
      } else if (size === 0) {
        // Box extends to end of data
        size = data.length - offset;
      }

      if (size < headerSize || offset + size > data.length) {
        break;
      }

      boxes.push({
        type,
        size,
        data: data.slice(offset + headerSize, offset + size)
      });

      offset += size;
    }

    return boxes;
  }

  /**
   * Decode an fMP4 media segment
   * @param {ArrayBuffer} segment - fMP4 media segment
   * @param {Object} options - Optional decoding options
   * @param {number} options.baseTimeSeconds - Override the segment's internal tfdt with this base time (in seconds).
   *                                           Use the manifest's startTime for accurate timing.
   * @returns {Promise<void>}
   */
  async decode(segment, options = {}) {
    if (!this.initialized || !this.decoder) {
      throw new Error('Decoder not initialized');
    }

    const data = new Uint8Array(segment);
    const samples = this.extractSamples(data, options.baseTimeSeconds);

    for (const sample of samples) {
      const chunk = new EncodedAudioChunk({
        type: 'key', // Opus frames are always keyframes
        timestamp: sample.timestamp,
        duration: sample.duration,
        data: sample.data
      });

      this.decoder.decode(chunk);
    }

    // Wait for decode to complete
    await this.decoder.flush();
  }

  /**
   * Extract audio samples from fMP4 segment
   * @param {Uint8Array} data - Segment data
   * @param {number} [baseTimeSecondsOverride] - Optional base time override in seconds (from manifest)
   * @returns {Array<{data: Uint8Array, timestamp: number, duration: number}>}
   */
  extractSamples(data, baseTimeSecondsOverride) {
    const samples = [];
    const boxes = this.parseMP4Boxes(data);

    // Find moof and mdat boxes
    const moof = boxes.find(b => b.type === 'moof');
    const mdat = boxes.find(b => b.type === 'mdat');

    if (!moof || !mdat) {
      console.warn('Missing moof or mdat box in segment');
      return samples;
    }

    // Parse moof to get sample info
    const moofChildren = this.parseMP4Boxes(moof.data);
    const traf = moofChildren.find(b => b.type === 'traf');
    if (!traf) {
      console.warn('No traf box found in moof');
      return samples;
    }

    const trafChildren = this.parseMP4Boxes(traf.data);

    // Get track fragment header
    const tfhd = trafChildren.find(b => b.type === 'tfhd');
    const tfdt = trafChildren.find(b => b.type === 'tfdt');
    const trun = trafChildren.find(b => b.type === 'trun');

    if (!trun) {
      console.warn('No trun box found');
      return samples;
    }

    // Parse tfhd for defaults
    let defaultSampleDuration = 960; // Default Opus frame size at 48kHz (20ms)
    let defaultSampleSize = 0;

    if (tfhd) {
      const tfhdView = new DataView(tfhd.data.buffer, tfhd.data.byteOffset);
      const flags = tfhdView.getUint32(0) & 0xFFFFFF;
      let tfhdOffset = 8; // version(1) + flags(3) + track_id(4)

      if (flags & 0x000001) tfhdOffset += 8; // base_data_offset
      if (flags & 0x000002) tfhdOffset += 4; // sample_description_index
      if (flags & 0x000008) {
        defaultSampleDuration = tfhdView.getUint32(tfhdOffset);
        tfhdOffset += 4;
      }
      if (flags & 0x000010) {
        defaultSampleSize = tfhdView.getUint32(tfhdOffset);
      }
    }

    // Determine base decode time
    // If baseTimeSecondsOverride is provided (from manifest), use it instead of segment's tfdt
    // This ensures accurate timing even if GStreamer produces segment-local tfdt values
    let baseDecodeTime = 0;
    let actualTfdt = 0;

    // First, read the actual tfdt from the segment
    if (tfdt) {
      const tfdtView = new DataView(tfdt.data.buffer, tfdt.data.byteOffset);
      const version = tfdt.data[0];
      if (version === 1) {
        actualTfdt = Number(tfdtView.getBigUint64(4));
      } else {
        actualTfdt = tfdtView.getUint32(4);
      }
    }

    // Use override if provided, otherwise use actual tfdt
    if (baseTimeSecondsOverride !== undefined) {
      // Convert seconds to sample count (timescale units)
      baseDecodeTime = Math.floor(baseTimeSecondsOverride * this.sampleRate);
      // Log comparison for debugging
      const actualTfdtSeconds = actualTfdt / this.sampleRate;
      if (Math.abs(actualTfdtSeconds - baseTimeSecondsOverride) > 0.1) {
        console.log(`[TfdtMismatch] manifest=${baseTimeSecondsOverride.toFixed(3)}s, segment_tfdt=${actualTfdtSeconds.toFixed(3)}s, diff=${(baseTimeSecondsOverride - actualTfdtSeconds).toFixed(3)}s`);
      }
    } else {
      baseDecodeTime = actualTfdt;
    }

    // Parse trun for sample entries
    const trunView = new DataView(trun.data.buffer, trun.data.byteOffset);
    const trunFlags = trunView.getUint32(0) & 0xFFFFFF;
    const sampleCount = trunView.getUint32(4);

    let trunOffset = 8;

    // Data offset (if present)
    let dataOffset = 0;
    if (trunFlags & 0x000001) {
      dataOffset = trunView.getInt32(trunOffset);
      trunOffset += 4;
    }

    // First sample flags (if present)
    if (trunFlags & 0x000004) {
      trunOffset += 4;
    }

    // Read sample entries
    let sampleOffset = 0;
    let currentTime = baseDecodeTime;

    for (let i = 0; i < sampleCount; i++) {
      let duration = defaultSampleDuration;
      let size = defaultSampleSize;

      if (trunFlags & 0x000100) {
        duration = trunView.getUint32(trunOffset);
        trunOffset += 4;
      }
      if (trunFlags & 0x000200) {
        size = trunView.getUint32(trunOffset);
        trunOffset += 4;
      }
      if (trunFlags & 0x000400) {
        trunOffset += 4; // sample_flags
      }
      if (trunFlags & 0x000800) {
        trunOffset += 4; // sample_composition_time_offset
      }

      if (size > 0 && sampleOffset + size <= mdat.data.length) {
        // Convert timescale (48000) to microseconds
        const timestampUs = Math.floor((currentTime * 1000000) / this.sampleRate);
        const durationUs = Math.floor((duration * 1000000) / this.sampleRate);

        samples.push({
          data: mdat.data.slice(sampleOffset, sampleOffset + size),
          timestamp: timestampUs,
          duration: durationUs
        });
      }

      sampleOffset += size;
      currentTime += duration;
    }

    return samples;
  }

  /**
   * Handle decoded audio output
   * @param {AudioData} audioData
   */
  handleDecodedAudio(audioData) {
    // Extract PCM samples from AudioData
    const numberOfChannels = audioData.numberOfChannels;
    const numberOfFrames = audioData.numberOfFrames;

    // Create buffers for each channel
    const buffers = [];
    for (let ch = 0; ch < numberOfChannels; ch++) {
      const buffer = new Float32Array(numberOfFrames);
      audioData.copyTo(buffer, { planeIndex: ch, format: 'f32-planar' });
      buffers.push(buffer);
    }

    this.outputBuffers.push(...buffers);

    this.dispatchEvent(new CustomEvent('samples', {
      detail: {
        samples: buffers,
        sampleRate: audioData.sampleRate,
        channels: numberOfChannels,
        frames: numberOfFrames
      }
    }));

    audioData.close();
  }

  /**
   * Handle decoder error
   * @param {DOMException} error
   */
  handleError(error) {
    console.error('AudioDecoder error:', error);
    this.dispatchEvent(new CustomEvent('error', { detail: { error } }));
  }

  /**
   * Get and clear buffered samples
   * @returns {Float32Array[]}
   */
  getBufferedSamples() {
    const samples = this.outputBuffers;
    this.outputBuffers = [];
    return samples;
  }

  /**
   * Reset decoder state
   */
  async reset() {
    if (this.decoder) {
      await this.decoder.reset();
      if (this.initData) {
        const config = this.parseInitSegment(this.initData);
        this.decoder.configure(config);
      }
    }
    this.outputBuffers = [];
  }

  /**
   * Clean up resources
   */
  destroy() {
    if (this.decoder) {
      this.decoder.close();
      this.decoder = null;
    }
    this.outputBuffers = [];
    this.initialized = false;
    this.initData = null;
    this.codecDescription = null;
  }
}
