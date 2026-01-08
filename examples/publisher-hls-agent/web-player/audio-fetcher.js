/**
 * Audio Segment Fetcher
 *
 * Fetches audio manifest (audio.json) and audio segments (audio*.m4s) from the server.
 * Handles segment buffering and provides segments to the decoder on demand.
 *
 * Features:
 * - Manifest parsing and validation
 * - Segment prefetching for smooth playback
 * - Retry logic for failed fetches
 * - Event-based notification system
 */

export class AudioFetcher extends EventTarget {
  /**
   * @param {string} baseUrl - Base URL for fetching audio files
   * @param {Object} options - Configuration options
   * @param {number} options.prefetchCount - Number of segments to prefetch ahead (default: 3)
   * @param {number} options.maxRetries - Maximum retry attempts for failed fetches (default: 3)
   * @param {number} options.retryDelay - Delay between retries in ms (default: 1000)
   */
  constructor(baseUrl, options = {}) {
    super();

    this.baseUrl = baseUrl.endsWith('/') ? baseUrl.slice(0, -1) : baseUrl;
    this.prefetchCount = options.prefetchCount ?? 3;
    this.maxRetries = options.maxRetries ?? 3;
    this.retryDelay = options.retryDelay ?? 1000;

    /** @type {AudioManifest|null} */
    this.manifest = null;

    /** @type {ArrayBuffer|null} */
    this.initSegment = null;

    /** @type {Map<number, ArrayBuffer>} */
    this.segmentCache = new Map();

    /** @type {Set<number>} */
    this.fetchingSegments = new Set();

    this.ready = false;
  }

  /**
   * Initialize the fetcher by loading manifest and init segment
   * @returns {Promise<AudioManifest>}
   */
  async init() {
    // Fetch manifest
    this.manifest = await this.fetchManifest();

    // Fetch init segment (only if specified in manifest)
    // With cmafmux, segments are self-contained and don't need separate init
    if (this.manifest.init) {
      this.initSegment = await this.fetchInitSegment();
    } else {
      // Segments are self-contained (each includes init data)
      this.initSegment = null;
    }

    this.ready = true;
    this.dispatchEvent(new CustomEvent('ready', { detail: { manifest: this.manifest } }));

    return this.manifest;
  }

  /**
   * Fetch and parse the audio manifest
   * @returns {Promise<AudioManifest>}
   */
  async fetchManifest() {
    const url = `${this.baseUrl}/audio.json`;
    const response = await this.fetchWithRetry(url);
    const manifest = await response.json();

    // Validate manifest
    if (!manifest.version || !manifest.codec || !manifest.segments) {
      throw new Error('Invalid audio manifest format');
    }

    return manifest;
  }

  /**
   * Fetch the fMP4 initialization segment
   * @returns {Promise<ArrayBuffer>}
   */
  async fetchInitSegment() {
    if (!this.manifest) {
      throw new Error('Manifest not loaded');
    }

    const url = `${this.baseUrl}/${this.manifest.init}`;
    const response = await this.fetchWithRetry(url);
    return response.arrayBuffer();
  }

  /**
   * Get a segment by index, fetching if necessary
   * @param {number} index - Segment index
   * @returns {Promise<ArrayBuffer>}
   */
  async getSegment(index) {
    if (!this.manifest) {
      throw new Error('Manifest not loaded');
    }

    if (index < 0 || index >= this.manifest.segments.length) {
      throw new Error(`Segment index ${index} out of range`);
    }

    // Check cache first
    if (this.segmentCache.has(index)) {
      return this.segmentCache.get(index);
    }

    // Fetch the segment
    const segment = await this.fetchSegment(index);

    // Trigger prefetch of upcoming segments
    this.prefetchSegments(index + 1);

    return segment;
  }

  /**
   * Fetch a specific segment
   * @param {number} index - Segment index
   * @returns {Promise<ArrayBuffer>}
   */
  async fetchSegment(index) {
    if (this.segmentCache.has(index)) {
      return this.segmentCache.get(index);
    }

    const segmentInfo = this.manifest.segments[index];
    if (!segmentInfo) {
      throw new Error(`Segment ${index} not found in manifest`);
    }

    const url = `${this.baseUrl}/${segmentInfo.file}`;
    const response = await this.fetchWithRetry(url);
    const data = await response.arrayBuffer();

    // Cache the segment
    this.segmentCache.set(index, data);

    this.dispatchEvent(new CustomEvent('segmentLoaded', {
      detail: { index, size: data.byteLength }
    }));

    return data;
  }

  /**
   * Prefetch upcoming segments
   * @param {number} startIndex - Starting index for prefetch
   */
  prefetchSegments(startIndex) {
    if (!this.manifest) return;

    for (let i = 0; i < this.prefetchCount; i++) {
      const index = startIndex + i;

      // Don't prefetch beyond available segments
      if (index >= this.manifest.segments.length) break;

      // Skip if already cached or being fetched
      if (this.segmentCache.has(index) || this.fetchingSegments.has(index)) {
        continue;
      }

      this.fetchingSegments.add(index);

      this.fetchSegment(index)
        .then(() => {
          this.fetchingSegments.delete(index);
        })
        .catch(err => {
          this.fetchingSegments.delete(index);
          console.warn(`Failed to prefetch segment ${index}:`, err);
        });
    }
  }

  /**
   * Fetch with retry logic
   * @param {string} url - URL to fetch
   * @param {number} attempt - Current attempt number
   * @returns {Promise<Response>}
   */
  async fetchWithRetry(url, attempt = 1) {
    try {
      const response = await fetch(url);
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${response.statusText}`);
      }
      return response;
    } catch (error) {
      if (attempt < this.maxRetries) {
        await this.delay(this.retryDelay * attempt);
        return this.fetchWithRetry(url, attempt + 1);
      }
      throw new Error(`Failed to fetch ${url} after ${this.maxRetries} attempts: ${error.message}`);
    }
  }

  /**
   * Get segment info by time position
   * @param {number} timeSeconds - Time position in seconds
   * @returns {{ index: number, segmentInfo: AudioSegment, offsetInSegment: number } | null}
   */
  getSegmentByTime(timeSeconds) {
    if (!this.manifest) return null;

    for (let i = 0; i < this.manifest.segments.length; i++) {
      const seg = this.manifest.segments[i];
      const segEnd = seg.startTime + seg.duration;

      if (timeSeconds >= seg.startTime && timeSeconds < segEnd) {
        return {
          index: i,
          segmentInfo: seg,
          offsetInSegment: timeSeconds - seg.startTime
        };
      }
    }

    return null;
  }

  /**
   * Get total duration from manifest
   * @returns {number} Total duration in seconds
   */
  getTotalDuration() {
    if (!this.manifest || this.manifest.segments.length === 0) {
      return 0;
    }

    const lastSeg = this.manifest.segments[this.manifest.segments.length - 1];
    return lastSeg.startTime + lastSeg.duration;
  }

  /**
   * Get the recording start time
   * @returns {Date|null}
   */
  getStartTime() {
    if (!this.manifest) return null;
    return new Date(this.manifest.startTime);
  }

  /**
   * Clear segment cache (useful for memory management)
   * @param {number} keepFromIndex - Keep segments from this index onwards
   */
  clearCache(keepFromIndex = 0) {
    for (const [index] of this.segmentCache) {
      if (index < keepFromIndex) {
        this.segmentCache.delete(index);
      }
    }
  }

  /**
   * Helper delay function
   * @param {number} ms
   * @returns {Promise<void>}
   */
  delay(ms) {
    return new Promise(resolve => setTimeout(resolve, ms));
  }

  /**
   * Clean up resources
   */
  destroy() {
    this.segmentCache.clear();
    this.fetchingSegments.clear();
    this.manifest = null;
    this.initSegment = null;
    this.ready = false;
  }
}

/**
 * @typedef {Object} AudioManifest
 * @property {number} version - Manifest version
 * @property {string} codec - Audio codec (e.g., "opus")
 * @property {number} sampleRate - Sample rate in Hz
 * @property {number} channels - Number of channels
 * @property {number} segmentDuration - Target segment duration
 * @property {number} maxMixerParticipants - Max participants for mixing
 * @property {string} startTime - ISO 8601 recording start time
 * @property {string} init - Init segment filename
 * @property {AudioSegment[]} segments - List of segments
 */

/**
 * @typedef {Object} AudioSegment
 * @property {number} index - Segment index
 * @property {string} file - Segment filename
 * @property {number} duration - Segment duration in seconds
 * @property {number} startTime - Start time relative to recording start
 * @property {number} [size] - File size in bytes
 */
