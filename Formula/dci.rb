class Dci < Formula
  desc "Cloud Intelligence™ CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "2.9.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.9.0/dci_2.9.0_darwin_arm64.tar.gz"
      sha256 "892c74e46ec5e774ab5fbebc9ed65187ba0ccc150dbb4cbe65a66027b23159d5"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.9.0/dci_2.9.0_darwin_amd64.tar.gz"
      sha256 "fb17b651c34217653bf5d40e02f98910213130c5d00e21a1555c06be90817644"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.9.0/dci_2.9.0_linux_arm64.tar.gz"
      sha256 "8891cfab4570f875c55a3309cc1b3ed37c73fefebb9e923850c76d1e76830dde"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.9.0/dci_2.9.0_linux_amd64.tar.gz"
      sha256 "442710d893f7acd78c608498064f8ec9fee5c27a7c53e48f58bc6982355b5970"
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
