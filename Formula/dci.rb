class Dci < Formula
  desc "DoiT Cloud Intelligence CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "1.5.1"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v1.5.1/dci_1.5.1_darwin_arm64.tar.gz"
      sha256 "15915720bcf5775c0931eaecbba90e368f81f8010d25cadaa55fb9af53b99aac"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v1.5.1/dci_1.5.1_darwin_amd64.tar.gz"
      sha256 "27900c1a580c8ebfd1fff7497bfd074018ea0dbc6dea5c99301d46d2a86ebbc3"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v1.5.1/dci_1.5.1_linux_arm64.tar.gz"
      sha256 "e29f1239b630e634c8af2fdf23a6ebde2d5eba032cda8634b7405cf9b31d721b"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v1.5.1/dci_1.5.1_linux_amd64.tar.gz"
      sha256 "53b7550778233ea8a0ccb0a8c6ac62298220b080804d7bb6238de60b1e66e908"
    end
  end

  def install
    bin.install "dci"
  end

  test do
    output = shell_output("#{bin}/dci --help")
    assert_match "DoiT Cloud Intelligence", output
  end
end
