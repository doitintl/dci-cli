class Dci < Formula
  desc "Cloud Intelligence™ CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "2.6.2"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.6.2/dci_2.6.2_darwin_arm64.tar.gz"
      sha256 "096132c25f44041e2eda5433cef0b3abd3c08278b29596b1fcf87d42082e52da"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.6.2/dci_2.6.2_darwin_amd64.tar.gz"
      sha256 "2830bf287ee704445a0c60b4fd96dd2eee52dbba458a03280d008c4a2bd9945d"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.6.2/dci_2.6.2_linux_arm64.tar.gz"
      sha256 "9c4afd33b0f94df225dda6045cf655b8f032944d3ab7ca30c3361e540573dfec"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.6.2/dci_2.6.2_linux_amd64.tar.gz"
      sha256 "b6bd0280b704d7e822e68419849f1cad2b77460496dd8ef22b1fa3571bfaa569"
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
