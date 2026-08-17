class Dci < Formula
  desc "Cloud Intelligence™ CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "2.1.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.1.0/dci_2.1.0_darwin_arm64.tar.gz"
      sha256 "a95e712f4071e5d0a5f0d185f0f2a96790daccbef02b5600bf4fdcc9a4ca5d2d"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.1.0/dci_2.1.0_darwin_amd64.tar.gz"
      sha256 "c1b88c788c1cae6990ea904056fa30507d94153092fc3b8bada0fa686b03b13e"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.1.0/dci_2.1.0_linux_arm64.tar.gz"
      sha256 "94b29e7758400a0a3577dd9644ffc40f50c301f5a34ed8a27a27d3db408c030a"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.1.0/dci_2.1.0_linux_amd64.tar.gz"
      sha256 "961be82e4537de032402ea33e0246bce2879d66c5ca59b767b44b5e9333b1c22"
    end
  end

  def install
    bin.install "dci"
    bash_completion.install "completions/dci.bash" => "dci"
    zsh_completion.install "completions/dci.zsh" => "_dci"
    fish_completion.install "completions/dci.fish"
  end

  test do
    output = shell_output("#{bin}/dci --help")
    assert_match "Cloud Intelligence™", output
  end
end
