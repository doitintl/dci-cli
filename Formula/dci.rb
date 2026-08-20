class Dci < Formula
  desc "Cloud Intelligence™ CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "2.5.1"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.5.1/dci_2.5.1_darwin_arm64.tar.gz"
      sha256 "e1bf25bc35eb4659532ff8f43056475987205fa2a3157edd8a1c2393ed5b70ab"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.5.1/dci_2.5.1_darwin_amd64.tar.gz"
      sha256 "a9d77b88c4833f4d2e45f0565fe97a1e2857fd1e228f7eb00fad8775f86e6c4e"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.5.1/dci_2.5.1_linux_arm64.tar.gz"
      sha256 "8cf263410314d908400be6951405698b1f39caebca21753fc9d857b4c837daca"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.5.1/dci_2.5.1_linux_amd64.tar.gz"
      sha256 "c700921537589ee2c6a2534ae4b3f17cc1aa68b6acc26cf28bfa8f67469a2665"
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
