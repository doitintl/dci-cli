class Dci < Formula
  desc "Cloud Intelligence™ CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "2.3.1"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.3.1/dci_2.3.1_darwin_arm64.tar.gz"
      sha256 "4baef5d0eeccb2e448d71628df44d242aa87afee4985a5a0f2095a1433084052"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.3.1/dci_2.3.1_darwin_amd64.tar.gz"
      sha256 "25409b6120479e41f9e472e6324dce2aa6605c01f2b3c020d232fddd5277cfce"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.3.1/dci_2.3.1_linux_arm64.tar.gz"
      sha256 "1b5ae17897dce5625106c1f2a08a6bd8f8ea8ce3368ab7301f4feedd48a335a1"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.3.1/dci_2.3.1_linux_amd64.tar.gz"
      sha256 "d15273c294dc4ed27242676baead7e061dbec7da5205e79a79179bea366b45f5"
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
