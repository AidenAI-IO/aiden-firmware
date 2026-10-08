#!/usr/bin/env python3
"""Exercise Wi-Fi boot recovery with isolated sysfs and command fixtures."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

REPO = Path(__file__).resolve().parents[1]
HELPER = REPO / "overlay-debian/usr/lib/aiden/aiden-wifi-prepare"


class WifiPrepareTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.driver = self.root / "sys/bus/platform/drivers/dwmmc_rockchip"
        self.gpio = self.root / "sys/class/gpio/gpio117"
        self.driver.mkdir(parents=True)
        (self.driver / "ff9a0000.mmc").touch()
        self.gpio.mkdir(parents=True)
        (self.gpio / "direction").write_text("in\n")
        (self.gpio / "value").write_text("1\n")
        model = self.root / "proc/device-tree/model"
        model.parent.mkdir(parents=True)
        model.write_bytes(b"Aiden SCH v1\0")
        self.helper = self.root / "prepare"
        self.helper.write_text(HELPER.read_text().replace("/sys/", str(self.root / "sys") + "/")
                               .replace("/proc/", str(self.root / "proc") + "/"))
        self.bin = self.root / "bin"
        self.bin.mkdir()
        sleep = self.bin / "sleep"
        sleep.write_text("""#!/usr/bin/env python3
import os, sys
from pathlib import Path
root = Path(os.environ["FIXTURE"])
driver = root / "sys/bus/platform/drivers/dwmmc_rockchip"
gpio = root / "sys/class/gpio/gpio117"
delay = sys.argv[1]
with (root / "sleeps").open("a") as f:
    f.write(delay + "\\n")
assert (driver / "unbind").read_text().strip() == "ff9a0000.mmc"
if delay == "1":
    assert (gpio / "direction").read_text().strip() == "low"
    assert not (driver / "bind").exists()
    if os.environ["SCENARIO"] == "interrupted":
        sys.exit(1)
elif delay == "2":
    assert (gpio / "value").read_text().strip() == "1"
    assert not (driver / "bind").exists()
elif delay == "0.1":
    assert (driver / "bind").read_text().strip() == "ff9a0000.mmc"
    scenario = os.environ["SCENARIO"]
    if scenario in ("success", "partial", "wrong-vendor"):
        for index, device in enumerate(("0x0082", "0x0182"), 1):
            if scenario == "partial" and index == 2:
                continue
            node = root / ("sys/bus/platform/devices/ff9a0000.mmc/"
                           "mmc_host/mmc2/mmc2:390b/mmc2:390b:" + str(index))
            node.mkdir(parents=True, exist_ok=True)
            (node / "vendor").write_text("0xffff" if scenario == "wrong-vendor" else "0xc8a1")
            (node / "device").write_text(device)
""")
        sleep.chmod(0o755)

    def run_helper(self, scenario="success"):
        return subprocess.run(["sh", str(self.helper)], text=True, capture_output=True,
                              env={**os.environ, "PATH": str(self.bin) + ":" + os.environ["PATH"],
                                   "FIXTURE": str(self.root), "SCENARIO": scenario},
                              timeout=15)

    def test_recovery_waits_for_both_functions_after_reset_and_reprobe(self):
        result = self.run_helper()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("result=success", result.stderr)
        self.assertEqual((self.root / "sleeps").read_text().splitlines(), ["1", "2", "0.1"])
        self.assertEqual((self.gpio / "value").read_text().strip(), "1")

    def test_other_board_untouched(self):
        (self.root / "proc/device-tree/model").write_text("Luckfox Pico Zero")
        self.assertEqual(self.run_helper().returncode, 0)
        self.assertFalse((self.driver / "unbind").exists())

    def test_live_interface_untouched(self):
        (self.root / "sys/class/net/wlan0").mkdir(parents=True)
        self.assertEqual(self.run_helper().returncode, 0)
        self.assertFalse((self.driver / "unbind").exists())

    def test_loaded_driver_without_interface_refuses_reset(self):
        (self.root / "sys/module/aic8800_bsp").mkdir(parents=True)
        result = self.run_helper()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("driver-already-loaded", result.stderr)
        self.assertFalse((self.driver / "unbind").exists())

    def test_missing_host_refuses_gpio_changes(self):
        (self.driver / "ff9a0000.mmc").unlink()
        self.assertNotEqual(self.run_helper().returncode, 0)
        self.assertEqual((self.gpio / "direction").read_text().strip(), "in")

    def test_timeout_partial_or_wrong_card_never_report_success(self):
        for scenario in ("absent", "partial", "wrong-vendor"):
            with self.subTest(scenario=scenario):
                result = self.run_helper(scenario)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("sdio-enumeration-timeout", result.stderr)
                self.assertEqual((self.gpio / "direction").read_text().strip(), "high")
                # Reinitialize files that the mock checks at the next reset.
                (self.driver / "bind").unlink()

    def test_failure_while_low_restores_enable_and_host(self):
        result = self.run_helper("interrupted")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((self.gpio / "direction").read_text().strip(), "high")
        self.assertEqual((self.driver / "bind").read_text().strip(), "ff9a0000.mmc")


if __name__ == "__main__":
    unittest.main()
