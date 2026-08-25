class Dci < Formula
  desc "Cloud Intelligence™ CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "2.6.1"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.6.1/dci_2.6.1_darwin_arm64.tar.gz"
      sha256 "bbe2aea50e287316a7e8cc46e2d42bef764ee75ae8893ac54a053c2c3ee79360"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.6.1/dci_2.6.1_darwin_amd64.tar.gz"
      sha256 "d63666efb8834f1afb46c825de6faaab733785abe66486d73b54b63aa169727d"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.6.1/dci_2.6.1_linux_arm64.tar.gz"
      sha256 "854ea153a6632910e988ab60f9ddc5bb58f4c9f2e7307e931607e47921e66183"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.6.1/dci_2.6.1_linux_amd64.tar.gz"
      sha256 "892e6297e78fc8adf576d15f9e9de58bbec65b29aba321347d109d88c344927b"
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
