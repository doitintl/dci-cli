class Dci < Formula
  desc "DoiT Cloud Intelligence CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "1.0.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v1.0.0/dci_1.0.0_darwin_arm64.tar.gz"
      sha256 "ac03b0b5de8abf9ca9fa5ce8d38687b3c2cbf30373ee0c4f7711760e580ddec1"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v1.0.0/dci_1.0.0_darwin_amd64.tar.gz"
      sha256 "d2d5fa77c3d177e52c29684822acbf01217ab90a910310abf49b688b332a5e63"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v1.0.0/dci_1.0.0_linux_arm64.tar.gz"
      sha256 "a0f6e1a18502b08435c0035df537cd525857e88b217aa7deddaa55cb2b67f62c"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v1.0.0/dci_1.0.0_linux_amd64.tar.gz"
      sha256 "8f62fc977f869f06588464e74cf46efabda07e6c2046510d2092270d88bd2443"
    end
  end

  def install
    bin.install "dci"
  end

  test do
    output = shell_output("#{bin}/dci --help")
    assert_match "DoiT Cloud Intelligence", output
  end
end
