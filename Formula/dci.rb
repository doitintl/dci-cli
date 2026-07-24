class Dci < Formula
  desc "DoiT Cloud Intelligence CLI"
  homepage "https://github.com/doitintl/dci-cli"
  version "1.5.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v1.5.0/dci_1.5.0_darwin_arm64.tar.gz"
      sha256 "da8b49adb5caf09017c8d997f5996b4755f08e03fe23cba91cb3de5027f7daf5"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v1.5.0/dci_1.5.0_darwin_amd64.tar.gz"
      sha256 "a6b22ec09592ab2c1f0e4c6d3fe625626120f5900de3af9497194257a2754328"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/doitintl/dci-cli/releases/download/v1.5.0/dci_1.5.0_linux_arm64.tar.gz"
      sha256 "2581ef352df637f687b77d22665fa351c8e9efc18b947914043d63e9b99685e7"
    else
      url "https://github.com/doitintl/dci-cli/releases/download/v1.5.0/dci_1.5.0_linux_amd64.tar.gz"
      sha256 "2ccb8def0f745f62603ee62a6c926783e7718fcce3ed818b95a7b0f84666791f"
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
