class Dci < Formula
  desc "Cloud Intelligence™ CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "2.4.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.4.0/dci_2.4.0_darwin_arm64.tar.gz"
      sha256 "8916bd2efeb1bf82b8bbf6748e279c201ebfd40bf431c8ab2c3d56a483a68a51"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.4.0/dci_2.4.0_darwin_amd64.tar.gz"
      sha256 "99259aaeb62750c5cb774dd46a4a7e0506573c5bf89665447788be94ca6dbce4"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.4.0/dci_2.4.0_linux_arm64.tar.gz"
      sha256 "199d86393f8f07554a58c4e77d715d8f4a42df68455399c1a53e7d118395929f"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.4.0/dci_2.4.0_linux_amd64.tar.gz"
      sha256 "cf043de3cf761d7204d7e3bbd7a6e7335106b071b24c31ea0d9d06911fd8444c"
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
