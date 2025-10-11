#!/usr/bin/env python3
"""
Video synchronization analyzer for HLS output.
Compares source video with HLS output video to detect drift and timing issues.
Works with Sync One2 test pattern (blinks every second starting from 10s).
"""

import subprocess
import numpy as np
import sys
import os
import json
import cv2
from scipy.signal import find_peaks

def extract_frame_brightness(video_file, output_file):
    """Extract average brightness of each frame using ffmpeg."""
    cmd = [
        'ffmpeg', '-i', video_file,
        '-vf', 'select=gt(scene\\,0),scale=128:72,format=gray,metadata=print:file=-',
        '-vsync', 'vfr',
        '-f', 'null',
        '-'
    ]

    # Alternative: use signalstats filter to get brightness values
    cmd = [
        'ffprobe', '-v', 'error',
        '-select_streams', 'v:0',
        '-show_entries', 'frame=pkt_pts_time',
        '-of', 'json',
        video_file
    ]

    result = subprocess.run(cmd, capture_output=True, text=True)
    if result.returncode != 0:
        print(f"Error extracting frame info from {video_file}:")
        print(result.stderr)
        return None, None

    # Parse JSON output
    data = json.loads(result.stdout)
    frame_times = []
    for frame in data.get('frames', []):
        if 'pkt_pts_time' in frame:
            frame_times.append(float(frame['pkt_pts_time']))

    return frame_times

def extract_brightness_timeline(video_file):
    """Extract brightness values over time using OpenCV."""
    cap = cv2.VideoCapture(video_file)

    if not cap.isOpened():
        print(f"Error: Could not open video file {video_file}")
        return None, None

    fps = cap.get(cv2.CAP_PROP_FPS)
    if fps == 0:
        fps = 24  # Default fallback

    brightness_values = []
    timestamps = []
    frame_count = 0

    while True:
        ret, frame = cap.read()
        if not ret:
            break

        # Convert to grayscale and calculate mean brightness
        gray = cv2.cvtColor(frame, cv2.COLOR_BGR2GRAY)
        brightness = np.mean(gray)

        brightness_values.append(brightness)
        timestamps.append(frame_count / fps)
        frame_count += 1

    cap.release()

    if len(timestamps) == 0:
        print(f"Warning: No frames extracted from {video_file}")
        return None, None

    return np.array(timestamps), np.array(brightness_values)

def detect_blinks(timestamps, brightness, threshold_percentile=30, min_distance_sec=0.5):
    """
    Detect blink positions in video (for Sync One2 test pattern).
    Returns array of blink positions in seconds.
    """
    if len(brightness) == 0:
        return np.array([])

    # Normalize brightness
    brightness_norm = (brightness - np.min(brightness)) / (np.max(brightness) - np.min(brightness))

    # Invert (blinks are dips in brightness)
    brightness_inverted = 1.0 - brightness_norm

    # Smooth the signal
    window_size = 5
    if len(brightness_inverted) > window_size:
        brightness_smooth = np.convolve(
            brightness_inverted,
            np.ones(window_size)/window_size,
            mode='same'
        )
    else:
        brightness_smooth = brightness_inverted

    # Calculate threshold from data
    threshold = np.percentile(brightness_smooth, 100 - threshold_percentile)

    # Estimate frame rate
    if len(timestamps) > 1:
        frame_duration = np.median(np.diff(timestamps))
        fps = 1.0 / frame_duration if frame_duration > 0 else 24
    else:
        fps = 24

    min_distance_frames = int(min_distance_sec * fps)

    # Find peaks (blinks)
    peaks, properties = find_peaks(
        brightness_smooth,
        height=threshold,
        distance=max(1, min_distance_frames)
    )

    # Convert to time
    blink_times = timestamps[peaks]

    return blink_times

def calculate_video_drift(source_blinks, output_blinks, tolerance=0.05):
    """
    Calculate drift between source and output video by comparing blink timings.
    Returns drift measurements at each blink position.
    """
    drifts = []

    min_length = min(len(source_blinks), len(output_blinks))

    for i in range(min_length):
        source_time = source_blinks[i]
        output_time = output_blinks[i]
        drift = output_time - source_time
        drifts.append({
            'blink_index': i,
            'source_time': source_time,
            'output_time': output_time,
            'drift_ms': drift * 1000
        })

    return drifts

def analyze_sync(source_file, hls_index_file):
    """Main analysis function."""
    print("=" * 70)
    print("Video Synchronization Analysis")
    print("=" * 70)

    # Extract brightness timelines
    print(f"\n1. Extracting brightness timeline from source: {source_file}")
    source_times, source_brightness = extract_brightness_timeline(source_file)

    if source_times is None or source_brightness is None:
        return

    print(f"   Source: {len(source_times)} frames, duration: {source_times[-1]:.2f}s")

    print(f"\n2. Extracting brightness timeline from HLS output: {hls_index_file}")
    output_times, output_brightness = extract_brightness_timeline(hls_index_file)

    if output_times is None or output_brightness is None:
        return

    print(f"   Output: {len(output_times)} frames, duration: {output_times[-1]:.2f}s")

    # Detect blinks
    print("\n3. Detecting blink pattern (Sync One2 test, starts at ~10s)...")
    source_blinks = detect_blinks(source_times, source_brightness, threshold_percentile=40)
    output_blinks = detect_blinks(output_times, output_brightness, threshold_percentile=40)

    print(f"   Source blinks detected: {len(source_blinks)}")
    print(f"   Output blinks detected: {len(output_blinks)}")

    if len(source_blinks) > 0:
        print(f"   First source blink at: {source_blinks[0]:.3f}s")
        if len(source_blinks) > 1:
            print(f"   Blink interval: {np.mean(np.diff(source_blinks)):.3f}s (expected: 1.0s)")

    if len(output_blinks) > 0:
        print(f"   First output blink at: {output_blinks[0]:.3f}s")
        if len(output_blinks) > 1:
            print(f"   Blink interval: {np.mean(np.diff(output_blinks)):.3f}s (expected: 1.0s)")

    # Calculate drift
    if len(source_blinks) > 0 and len(output_blinks) > 0:
        print("\n4. Analyzing drift between source and output...")
        drifts = calculate_video_drift(source_blinks, output_blinks)

        if drifts:
            print(f"\n   {'Blink #':<8} {'Source(s)':<12} {'Output(s)':<12} {'Drift(ms)':<12}")
            print("   " + "-" * 50)

            # Show first 10, middle, and last 10 blinks
            indices_to_show = []
            if len(drifts) <= 20:
                indices_to_show = range(len(drifts))
            else:
                indices_to_show = list(range(10)) + [len(drifts)//2] + list(range(len(drifts)-10, len(drifts)))

            for i in indices_to_show:
                d = drifts[i]
                print(f"   {d['blink_index']:<8} {d['source_time']:<12.3f} {d['output_time']:<12.3f} {d['drift_ms']:<12.1f}")
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

            # Check if drift is increasing (video running faster/slower)
            if len(drift_values) > 5:
                # Linear regression to detect trend
                x = np.arange(len(drift_values))
                y = np.array(drift_values)
                coeffs = np.polyfit(x, y, 1)
                drift_rate = coeffs[0]  # ms per blink

                print(f"   - Drift rate: {drift_rate:.2f}ms/blink ({drift_rate:.2f}ms/s)")

                if abs(drift_rate) < 1.0:
                    print(f"   ✓ Excellent: Drift rate is stable (< 1ms/s)")
                elif abs(drift_rate) < 5.0:
                    print(f"   ⚠ Warning: Drift rate is {drift_rate:.2f}ms/s (should be < 1ms/s)")
                else:
                    print(f"   ✗ Error: Significant drift rate {drift_rate:.2f}ms/s")

    print("\n" + "=" * 70)
    print("Analysis complete")
    print("=" * 70)

if __name__ == "__main__":
    if len(sys.argv) != 3:
        print("Usage: python analyze_video_sync.py <source.mp4> <hls_output/index.m3u8>")
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
