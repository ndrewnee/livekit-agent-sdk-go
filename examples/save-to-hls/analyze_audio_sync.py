#!/usr/bin/env python3
"""
Audio synchronization analyzer for HLS output.
Compares source audio with HLS output audio to detect drift and timing issues.
Works with Sync One2 test pattern (beeps every second).
"""

import subprocess
import numpy as np
import sys
import os
from scipy.io import wavfile
from scipy.signal import correlate, find_peaks

def extract_audio_to_wav(input_file, output_wav):
    """Extract audio from video/HLS to WAV format using ffmpeg."""
    cmd = [
        'ffmpeg', '-i', input_file,
        '-vn',  # No video
        '-acodec', 'pcm_s16le',  # PCM 16-bit
        '-ar', '48000',  # 48kHz sample rate
        '-ac', '1',  # Mono
        '-y',  # Overwrite
        output_wav
    ]

    result = subprocess.run(cmd, capture_output=True, text=True)
    if result.returncode != 0:
        print(f"Error extracting audio from {input_file}:")
        print(result.stderr)
        return False
    return True

def load_audio(wav_file):
    """Load WAV file and return sample rate and audio data."""
    try:
        sample_rate, audio_data = wavfile.read(wav_file)
        # Convert to float and normalize
        if audio_data.dtype == np.int16:
            audio_data = audio_data.astype(np.float32) / 32768.0
        elif audio_data.dtype == np.int32:
            audio_data = audio_data.astype(np.float32) / 2147483648.0
        return sample_rate, audio_data
    except Exception as e:
        print(f"Error loading {wav_file}: {e}")
        return None, None

def detect_beeps(audio_data, sample_rate, threshold=0.1):
    """
    Detect beep positions in audio (for Sync One2 test pattern).
    Returns array of beep positions in seconds.
    """
    # Calculate envelope (absolute value smoothed)
    envelope = np.abs(audio_data)

    # Smooth the envelope
    window_size = int(sample_rate * 0.01)  # 10ms window
    envelope_smooth = np.convolve(envelope, np.ones(window_size)/window_size, mode='same')

    # Find peaks (beeps)
    peaks, properties = find_peaks(
        envelope_smooth,
        height=threshold,
        distance=int(sample_rate * 0.5)  # At least 0.5s between beeps
    )

    # Convert sample positions to time in seconds
    beep_times = peaks / sample_rate

    return beep_times

def calculate_audio_drift(source_beeps, output_beeps, tolerance=0.05):
    """
    Calculate drift between source and output audio by comparing beep timings.
    Returns drift measurements at each beep position.
    """
    # Match beeps between source and output
    drifts = []

    min_length = min(len(source_beeps), len(output_beeps))

    for i in range(min_length):
        source_time = source_beeps[i]
        output_time = output_beeps[i]
        drift = output_time - source_time
        drifts.append({
            'beep_index': i,
            'source_time': source_time,
            'output_time': output_time,
            'drift_ms': drift * 1000
        })

    return drifts

def cross_correlate_audio(source_audio, output_audio, sample_rate):
    """
    Use cross-correlation to find the time offset between source and output.
    Returns offset in seconds.
    """
    # Limit to first 30 seconds for faster computation
    max_samples = int(sample_rate * 30)
    source_segment = source_audio[:min(max_samples, len(source_audio))]
    output_segment = output_audio[:min(max_samples, len(output_audio))]

    # Compute cross-correlation
    correlation = correlate(output_segment, source_segment, mode='same')

    # Find peak
    peak_index = np.argmax(np.abs(correlation))
    center = len(correlation) // 2
    offset_samples = peak_index - center
    offset_seconds = offset_samples / sample_rate

    return offset_seconds

def analyze_sync(source_file, hls_index_file):
    """Main analysis function."""
    print("=" * 70)
    print("Audio Synchronization Analysis")
    print("=" * 70)

    # Create temp directory for WAV files
    temp_dir = "/tmp/audio_sync_analysis"
    os.makedirs(temp_dir, exist_ok=True)

    source_wav = os.path.join(temp_dir, "source.wav")
    output_wav = os.path.join(temp_dir, "output.wav")

    # Extract audio from source
    print(f"\n1. Extracting audio from source: {source_file}")
    if not extract_audio_to_wav(source_file, source_wav):
        return

    # Extract audio from HLS output
    print(f"2. Extracting audio from HLS output: {hls_index_file}")
    if not extract_audio_to_wav(hls_index_file, output_wav):
        return

    # Load audio data
    print("\n3. Loading audio data...")
    source_sr, source_audio = load_audio(source_wav)
    output_sr, output_audio = load_audio(output_wav)

    if source_audio is None or output_audio is None:
        return

    print(f"   Source: {len(source_audio)/source_sr:.2f}s @ {source_sr}Hz")
    print(f"   Output: {len(output_audio)/output_sr:.2f}s @ {output_sr}Hz")

    # Detect beeps
    print("\n4. Detecting beep pattern (Sync One2 test)...")
    source_beeps = detect_beeps(source_audio, source_sr, threshold=0.05)
    output_beeps = detect_beeps(output_audio, output_sr, threshold=0.05)

    print(f"   Source beeps detected: {len(source_beeps)}")
    print(f"   Output beeps detected: {len(output_beeps)}")

    if len(source_beeps) > 0:
        print(f"   First source beep at: {source_beeps[0]:.3f}s")
        if len(source_beeps) > 1:
            print(f"   Beep interval: {np.mean(np.diff(source_beeps)):.3f}s (expected: 1.0s)")

    if len(output_beeps) > 0:
        print(f"   First output beep at: {output_beeps[0]:.3f}s")
        if len(output_beeps) > 1:
            print(f"   Beep interval: {np.mean(np.diff(output_beeps)):.3f}s (expected: 1.0s)")

    # Calculate drift
    if len(source_beeps) > 0 and len(output_beeps) > 0:
        print("\n5. Analyzing drift between source and output...")
        drifts = calculate_audio_drift(source_beeps, output_beeps)

        if drifts:
            print(f"\n   {'Beep #':<8} {'Source(s)':<12} {'Output(s)':<12} {'Drift(ms)':<12}")
            print("   " + "-" * 50)

            # Show first 10, middle, and last 10 beeps
            indices_to_show = []
            if len(drifts) <= 20:
                indices_to_show = range(len(drifts))
            else:
                indices_to_show = list(range(10)) + [len(drifts)//2] + list(range(len(drifts)-10, len(drifts)))

            for i in indices_to_show:
                d = drifts[i]
                print(f"   {d['beep_index']:<8} {d['source_time']:<12.3f} {d['output_time']:<12.3f} {d['drift_ms']:<12.1f}")
                if i == 9 and len(drifts) > 20:
                    print("   ...")

            # Calculate statistics
            drift_values = [d['drift_ms'] for d in drifts]
            print(f"\n   Drift Statistics:")
            print(f"   - Initial offset: {drift_values[0]:.1f}ms")
            print(f"   - Mean drift: {np.mean(drift_values):.1f}ms")
            print(f"   - Std deviation: {np.std(drift_values):.1f}ms")
            print(f"   - Min drift: {np.min(drift_values):.1f}ms")
            print(f"   - Max drift: {np.max(drift_values):.1f}ms")

            # Check if drift is increasing (audio running faster/slower)
            if len(drift_values) > 5:
                # Linear regression to detect trend
                x = np.arange(len(drift_values))
                y = np.array(drift_values)
                coeffs = np.polyfit(x, y, 1)
                drift_rate = coeffs[0]  # ms per beep

                print(f"   - Drift rate: {drift_rate:.2f}ms/beep ({drift_rate:.2f}ms/s)")

                if abs(drift_rate) < 1.0:
                    print(f"   ✓ Excellent: Drift rate is stable (< 1ms/s)")
                elif abs(drift_rate) < 5.0:
                    print(f"   ⚠ Warning: Drift rate is {drift_rate:.2f}ms/s (should be < 1ms/s)")
                else:
                    print(f"   ✗ Error: Significant drift rate {drift_rate:.2f}ms/s")

    # Cross-correlation analysis
    print("\n6. Cross-correlation analysis...")
    offset = cross_correlate_audio(source_audio, output_audio, source_sr)
    print(f"   Time offset between source and output: {offset*1000:.1f}ms")

    if abs(offset) < 0.050:
        print(f"   ✓ Excellent: Offset is < 50ms")
    elif abs(offset) < 0.100:
        print(f"   ⚠ Good: Offset is < 100ms")
    else:
        print(f"   ✗ Warning: Offset is {abs(offset)*1000:.1f}ms (> 100ms)")

    print("\n" + "=" * 70)
    print("Analysis complete")
    print("=" * 70)

    # Cleanup
    os.remove(source_wav)
    os.remove(output_wav)

if __name__ == "__main__":
    if len(sys.argv) != 3:
        print("Usage: python analyze_audio_sync.py <source.mp4> <hls_output/index.m3u8>")
        sys.exit(1)

    source_file = sys.argv[1]
    hls_file = sys.argv[2]

    if not os.path.exists(source_file):
        print(f"Error: Source file not found: {source_file}")
        sys.exit(1)

    if not os.path.exists(hls_file):
        print(f"Error: HLS file not found: {hls_file}")
        sys.exit(1)

    analyze_sync(source_file, hls_file)
