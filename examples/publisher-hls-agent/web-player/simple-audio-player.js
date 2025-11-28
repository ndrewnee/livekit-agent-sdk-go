/**
 * Simple Audio Player using HLS.js
 *
 * Uses HLS.js to play audio.m3u8 segments, avoiding custom fMP4 parsing.
 * Syncs audio playback with video element.
 */

export class SimpleAudioPlayer extends EventTarget {
  /**
   * @param {string} baseUrl - Base URL for audio files
   * @param {HTMLVideoElement} videoElement - Video element to sync with
   * @param {AudioContext} audioContext - Shared AudioContext
   */
  constructor(baseUrl, videoElement, audioContext) {
    super();

    // Normalize baseUrl
    this.baseUrl = baseUrl.endsWith('/') ? baseUrl.slice(0, -1) : baseUrl;
    if (this.baseUrl.match(/\.(m3u8|json|ts|m4s|mp4)$/i)) {
      this.baseUrl = this.baseUrl.substring(0, this.baseUrl.lastIndexOf('/'));
    }

    this.videoElement = videoElement;
    this.audioContext = audioContext;

    /** @type {HTMLAudioElement} */
    this.audioElement = null;

    /** @type {Object} */
    this.hls = null;

    /** @type {MediaElementAudioSourceNode} */
    this.sourceNode = null;

    /** @type {GainNode} */
    this.gainNode = null;

    this.isReady = false;
    this.isSyncing = false;

    // Bind event handlers
    this._onVideoSeeking = this._onVideoSeeking.bind(this);
    this._onVideoSeeked = this._onVideoSeeked.bind(this);
    this._onVideoPlay = this._onVideoPlay.bind(this);
    this._onVideoPause = this._onVideoPause.bind(this);
    this._onVideoTimeUpdate = this._onVideoTimeUpdate.bind(this);
  }

  /**
   * Initialize the audio player
   * @returns {Promise<void>}
   */
  async init() {
    // Create hidden audio element
    this.audioElement = document.createElement('audio');
    this.audioElement.style.display = 'none';
    this.audioElement.crossOrigin = 'anonymous';
    this.audioElement.preload = 'auto';
    document.body.appendChild(this.audioElement);

    // Create gain node for volume control
    this.gainNode = this.audioContext.createGain();
    this.gainNode.gain.value = 1.0;

    // Load audio using HLS.js or native
    const audioUrl = `${this.baseUrl}/audio.m3u8`;

    if (window.Hls && window.Hls.isSupported()) {
      this.hls = new window.Hls({
        enableWorker: true,
        lowLatencyMode: false
      });

      this.hls.loadSource(audioUrl);
      this.hls.attachMedia(this.audioElement);

      await new Promise((resolve, reject) => {
        this.hls.once(window.Hls.Events.MANIFEST_PARSED, resolve);
        this.hls.once(window.Hls.Events.ERROR, (event, data) => {
          if (data.fatal) reject(new Error(data.error));
        });
      });
    } else if (this.audioElement.canPlayType('application/vnd.apple.mpegurl')) {
      // Native HLS (Safari)
      this.audioElement.src = audioUrl;
      await new Promise(resolve => {
        this.audioElement.addEventListener('loadedmetadata', resolve, { once: true });
      });
    } else {
      throw new Error('HLS not supported for audio');
    }

    // Connect audio element to Web Audio for mixing support
    this.sourceNode = this.audioContext.createMediaElementSource(this.audioElement);
    this.sourceNode.connect(this.gainNode);

    // Setup video sync events
    this.videoElement.addEventListener('seeking', this._onVideoSeeking);
    this.videoElement.addEventListener('seeked', this._onVideoSeeked);
    this.videoElement.addEventListener('play', this._onVideoPlay);
    this.videoElement.addEventListener('pause', this._onVideoPause);
    this.videoElement.addEventListener('timeupdate', this._onVideoTimeUpdate);

    this.isReady = true;
    this.dispatchEvent(new CustomEvent('ready'));
  }

  /**
   * Connect to audio destination (for mixing)
   * @param {AudioNode} destination
   */
  connect(destination) {
    this.gainNode.connect(destination);
  }

  /**
   * Disconnect from all destinations
   */
  disconnect() {
    this.gainNode.disconnect();
  }

  /**
   * Set volume
   * @param {number} volume - 0 to 1
   */
  setVolume(volume) {
    this.gainNode.gain.value = Math.max(0, Math.min(1, volume));
  }

  /**
   * Sync audio to video position
   * @private
   */
  _syncToVideo() {
    if (!this.isReady || this.isSyncing) return;

    const videoTime = this.videoElement.currentTime;
    const audioTime = this.audioElement.currentTime;
    const drift = Math.abs(videoTime - audioTime);

    // If drift is more than 150ms, resync
    if (drift > 0.15) {
      this.isSyncing = true;
      this.audioElement.currentTime = videoTime;
      // Small delay to let the seek complete
      setTimeout(() => {
        this.isSyncing = false;
      }, 50);
    }
  }

  _onVideoSeeking() {
    // Pause audio during seek to prevent desync
    if (!this.audioElement.paused) {
      this.audioElement.pause();
    }
  }

  _onVideoSeeked() {
    // Sync audio to new position
    this.audioElement.currentTime = this.videoElement.currentTime;
    // Resume if video is playing
    if (!this.videoElement.paused) {
      this.audioElement.play().catch(e => console.warn('Audio play failed:', e));
    }
  }

  _onVideoPlay() {
    // Sync and start audio
    this.audioElement.currentTime = this.videoElement.currentTime;
    this.audioElement.play().catch(e => console.warn('Audio play failed:', e));
  }

  _onVideoPause() {
    this.audioElement.pause();
  }

  _onVideoTimeUpdate() {
    this._syncToVideo();
  }

  /**
   * Clean up resources
   */
  destroy() {
    // Remove event listeners
    this.videoElement.removeEventListener('seeking', this._onVideoSeeking);
    this.videoElement.removeEventListener('seeked', this._onVideoSeeked);
    this.videoElement.removeEventListener('play', this._onVideoPlay);
    this.videoElement.removeEventListener('pause', this._onVideoPause);
    this.videoElement.removeEventListener('timeupdate', this._onVideoTimeUpdate);

    // Stop and disconnect audio
    if (this.audioElement) {
      this.audioElement.pause();
      this.audioElement.remove();
    }

    if (this.hls) {
      this.hls.destroy();
    }

    if (this.sourceNode) {
      this.sourceNode.disconnect();
    }

    if (this.gainNode) {
      this.gainNode.disconnect();
    }

    this.isReady = false;
  }
}
