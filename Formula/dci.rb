class Dci < Formula
  desc "Cloud Intelligence™ CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "2.0.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.0.0/dci_2.0.0_darwin_arm64.tar.gz"
      sha256 "936b2ba9eb0e7f94dfd0bcfd1a750e4cafbf25541d3d07e7e37a6548b804ef9a"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.0.0/dci_2.0.0_darwin_amd64.tar.gz"
      sha256 "fa10c07dc5d85ad1d8c904158597f352fa241d099027f80c5572df89f1abddea"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.0.0/dci_2.0.0_linux_arm64.tar.gz"
      sha256 "0145e63dfa2da955606d3290a4c6348cdc695c252ae9282cfe6428af98fca523"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.0.0/dci_2.0.0_linux_amd64.tar.gz"
      sha256 "ebcc16041392b47527dec3c23f1f7566db9026711a9145dd741bb56ecd813aa8"
    end
  end

  def install
    bin.install "dci"
  end

  test do
    output = shell_output("#{bin}/dci --help")
    assert_match "Cloud Intelligence™", output
  end
end
