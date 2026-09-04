class Dci < Formula
  desc "Cloud Intelligence™ CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "2.7.5"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.5/dci_2.7.5_darwin_arm64.tar.gz"
      sha256 "6e94fe95650cb50feaf726577db8ece8d548e2846d66de289f69cda461b6058d"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.5/dci_2.7.5_darwin_amd64.tar.gz"
      sha256 "182ffe930638c905ea4f8498c324af508e1e49e5ef3f857531910b8a2c12395b"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.5/dci_2.7.5_linux_arm64.tar.gz"
      sha256 "03176ae880da7fa3797d3656bf8543428139c443ca7567a93994e5d71c7c01a0"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.5/dci_2.7.5_linux_amd64.tar.gz"
      sha256 "0c36e36f93cd7448723a804f52347500e0e23cfeb4ae403988727ca985444ea4"
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
