class Dci < Formula
  desc "Cloud Intelligence™ CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "2.2.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.2.0/dci_2.2.0_darwin_arm64.tar.gz"
      sha256 "dcf617aea6ed4190e118c73821cdf9ce64bd1d485d5cb4064b05899f67e08caa"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.2.0/dci_2.2.0_darwin_amd64.tar.gz"
      sha256 "6451211f80a57d40cca0f50cb8a6fd06fc4999a0b3d9ab16a08b2629b39638c4"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v2.2.0/dci_2.2.0_linux_arm64.tar.gz"
      sha256 "68da4028f1a776a84d33c14872bae2b4289f92e5772575701b88e0ffe8a050e9"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v2.2.0/dci_2.2.0_linux_amd64.tar.gz"
      sha256 "d51e32855622211bdc53fe5db3639f00575d7d586eddb253351524090577d0d6"
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
