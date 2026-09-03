class Dci < Formula
  desc "Cloud Intelligence™ CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "2.7.3"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.3/dci_2.7.3_darwin_arm64.tar.gz"
      sha256 "fba3995f5a1693b7089eb4698d2d85efdbe63ba0db991481b2d2332207815a6c"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.3/dci_2.7.3_darwin_amd64.tar.gz"
      sha256 "f1d8e694af3029c8fbabbaa36d90e7ad808a5b3d1270ccae3e772c73ec506cae"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.3/dci_2.7.3_linux_arm64.tar.gz"
      sha256 "86c45fcebb793d005bc9045bf0d99319b21a0a63e63792999b3e94cb7c92abc8"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.7.3/dci_2.7.3_linux_amd64.tar.gz"
      sha256 "5f2bc732dd175710ed34900b6f3e8d4725ebd30c308ebdce2367905f72c3a49c"
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
