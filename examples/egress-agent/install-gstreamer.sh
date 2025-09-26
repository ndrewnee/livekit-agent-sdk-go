#!/bin/bash

# GStreamer Installation Script for Egress Agent
# Supports macOS, Ubuntu/Debian, and RHEL/CentOS

set -e

echo "================================"
echo "GStreamer Installation Script"
echo "================================"

# Detect OS
if [[ "$OSTYPE" == "darwin"* ]]; then
    OS="macos"
elif [[ -f /etc/debian_version ]]; then
    OS="debian"
elif [[ -f /etc/redhat-release ]]; then
    OS="rhel"
else
    echo "Unsupported operating system"
    exit 1
fi

echo "Detected OS: $OS"

# Function to check if GStreamer is installed
check_gstreamer() {
    if command -v gst-launch-1.0 &> /dev/null; then
        VERSION=$(gst-launch-1.0 --version | grep "GStreamer" | awk '{print $2}')
        echo "GStreamer $VERSION is already installed"

        # Check if version is 1.20 or higher
        MAJOR=$(echo $VERSION | cut -d. -f1)
        MINOR=$(echo $VERSION | cut -d. -f2)

        if [[ $MAJOR -gt 1 ]] || [[ $MAJOR -eq 1 && $MINOR -ge 20 ]]; then
            echo "GStreamer version is sufficient (>= 1.20)"
            return 0
        else
            echo "GStreamer version is too old (< 1.20), need to upgrade"
            return 1
        fi
    else
        echo "GStreamer is not installed"
        return 1
    fi
}

# Function to verify required plugins
verify_plugins() {
    echo ""
    echo "Verifying required GStreamer plugins..."

    REQUIRED_PLUGINS=(
        "hlssink2"        # HLS output
        "rtpbin"          # RTP handling
        "rtph264depay"    # H.264 RTP depayloading
        "rtpopusdepay"    # Opus RTP depayloading
        "h264parse"       # H.264 parsing
        "opusparse"       # Opus parsing
        "mpegtsmux"       # MPEG-TS muxing
        "videorate"       # Video gap filling
        "audiorate"       # Audio gap filling
        "avenc_aac"       # AAC encoding (optional)
        "lamemp3enc"      # MP3 encoding (optional)
        "jpegenc"         # JPEG encoding (screenshots)
    )

    MISSING_PLUGINS=()

    for plugin in "${REQUIRED_PLUGINS[@]}"; do
        if gst-inspect-1.0 $plugin &> /dev/null; then
            echo "✓ $plugin found"
        else
            echo "✗ $plugin missing"
            MISSING_PLUGINS+=($plugin)
        fi
    done

    if [ ${#MISSING_PLUGINS[@]} -eq 0 ]; then
        echo ""
        echo "All required plugins are installed!"
        return 0
    else
        echo ""
        echo "Missing plugins: ${MISSING_PLUGINS[@]}"
        return 1
    fi
}

# Install for macOS
install_macos() {
    echo "Installing GStreamer for macOS..."

    # Check if Homebrew is installed
    if ! command -v brew &> /dev/null; then
        echo "Homebrew is not installed. Please install it first:"
        echo "/bin/bash -c \"\$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)\""
        exit 1
    fi

    echo "Updating Homebrew..."
    brew update

    echo "Installing GStreamer and plugins..."
    brew install gstreamer gst-plugins-base gst-plugins-good gst-plugins-bad gst-plugins-ugly gst-libav

    echo "GStreamer installation complete for macOS"
}

# Install for Ubuntu/Debian
install_debian() {
    echo "Installing GStreamer for Ubuntu/Debian..."

    # Update package list
    sudo apt-get update

    # Install GStreamer and required plugins
    sudo apt-get install -y \
        gstreamer1.0-tools \
        gstreamer1.0-plugins-base \
        gstreamer1.0-plugins-good \
        gstreamer1.0-plugins-bad \
        gstreamer1.0-plugins-ugly \
        gstreamer1.0-libav \
        libgstreamer1.0-dev \
        libgstreamer-plugins-base1.0-dev

    echo "GStreamer installation complete for Ubuntu/Debian"
}

# Install for RHEL/CentOS
install_rhel() {
    echo "Installing GStreamer for RHEL/CentOS..."

    # Enable EPEL repository
    sudo yum install -y epel-release

    # Install GStreamer and required plugins
    sudo yum install -y \
        gstreamer1 \
        gstreamer1-plugins-base \
        gstreamer1-plugins-good \
        gstreamer1-plugins-bad-free \
        gstreamer1-plugins-ugly \
        gstreamer1-libav \
        gstreamer1-devel \
        gstreamer1-plugins-base-devel

    echo "GStreamer installation complete for RHEL/CentOS"
}

# Main installation flow
main() {
    # Check if GStreamer is already installed with correct version
    if check_gstreamer; then
        # Verify plugins even if GStreamer is installed
        if verify_plugins; then
            echo ""
            echo "================================"
            echo "GStreamer is ready to use!"
            echo "================================"
            exit 0
        fi
    fi

    # Install based on OS
    case $OS in
        macos)
            install_macos
            ;;
        debian)
            install_debian
            ;;
        rhel)
            install_rhel
            ;;
    esac

    # Verify installation
    echo ""
    echo "Verifying installation..."

    if check_gstreamer; then
        verify_plugins
        echo ""
        echo "================================"
        echo "Installation completed successfully!"
        echo "================================"

        # Show test command
        echo ""
        echo "You can test the installation with:"
        echo "gst-launch-1.0 videotestsrc ! autovideosink"
    else
        echo "Installation failed. Please check the error messages above."
        exit 1
    fi
}

# Run main function
main