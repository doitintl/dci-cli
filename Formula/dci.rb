class Dci < Formula
  desc "Cloud Intelligence™ CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "2.5.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.5.0/dci_2.5.0_darwin_arm64.tar.gz"
      sha256 "537342f934675dbf70b1700a1b5b8b6d4076718a6bd73694a45734e68352d649"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.5.0/dci_2.5.0_darwin_amd64.tar.gz"
      sha256 "c026171e0cacfd5eed804206a84657d80ba0cbc60411e4f1c1b93de8ec1234d1"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.5.0/dci_2.5.0_linux_arm64.tar.gz"
      sha256 "f1408fc39c3c88eb1fc124297a77d3e079db555264d796c6d25450bc9b772e4c"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.5.0/dci_2.5.0_linux_amd64.tar.gz"
      sha256 "72876d68ab16da95e6f694ccff506fdc4dc1716371503984754488befb6087be"
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
