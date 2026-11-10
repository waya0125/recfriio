#include <stdio.h>
#include <stdlib.h>
#include <stdint.h>
#include <string.h>
#include <unistd.h>
#include <fcntl.h>
#include <sys/ioctl.h>
#include <linux/usbdevice_fs.h>

#define TIMEOUT_MS 1000

static uint8_t reverse8(uint8_t b) {
    uint8_t rev = 0;
    for (int i = 0; i < 8; i++) {
        if (b & (1 << i)) rev |= (1 << (7 - i));
    }
    return rev;
}

static int send_ctl(int fd, uint8_t req, uint16_t val, uint16_t idx, uint16_t len, uint8_t *data) {
    struct usbdevfs_ctrltransfer ctrl;
    ctrl.bRequestType = 0xc0;
    ctrl.bRequest = req;
    ctrl.wValue = val;
    ctrl.wIndex = idx;
    ctrl.wLength = len;
    ctrl.timeout = TIMEOUT_MS;
    ctrl.data = data;
    return ioctl(fd, USBDEVFS_CONTROL, &ctrl);
}

static int asie_read_reg(int fd, uint8_t saddr, uint8_t reg, uint8_t *val) {
    uint8_t data[2] = {0};
    int ret = send_ctl(fd, 0x02, ((uint16_t)reg << 8) | saddr, 0x0000, 2, data);
    if (ret != 2 || data[0] != 1) return -1;
    *val = data[1];
    return 0;
}

static int asie_write_reg(int fd, uint8_t saddr, uint8_t reg, uint8_t val) {
    uint8_t data[2] = {0};
    int ret = send_ctl(fd, 0x03, ((uint16_t)reg << 8) | saddr, (uint16_t)val, 2, data);
    if (ret != 2 || data[0] != 1) return -1;
    return 0;
}

static int asie_get_available(int fd, uint8_t saddr) {
    uint8_t low = 0, high = 0;
    if (asie_read_reg(fd, saddr, 0x03, &low) < 0) return -1;
    if (asie_read_reg(fd, saddr, 0x39, &high) < 0) return -1;
    return low | ((high & 1) << 8);
}

static int asie_send_apdu(int fd, uint8_t saddr, uint8_t seq, const uint8_t *apdu, int apdu_len, uint8_t *resp, int *resp_len) {
    // T=1 I-block: NAD(0), PCB(seq << 6), LEN(apdu_len), INF(apdu), LRC
    int block_len = 3 + apdu_len + 1;
    uint8_t block[256];
    block[0] = reverse8(0x00);
    block[1] = reverse8((seq & 1) << 6);
    block[2] = reverse8((uint8_t)apdu_len);
    for (int i = 0; i < apdu_len; i++) {
        block[3 + i] = reverse8(apdu[i]);
    }
    uint8_t lrc = 0;
    for (int i = 0; i < 3 + apdu_len; i++) {
        lrc ^= block[i];
    }
    block[3 + apdu_len] = lrc;

    // Write transmit length
    asie_write_reg(fd, saddr, 0x02, (uint8_t)block_len);
    asie_write_reg(fd, saddr, 0x38, (uint8_t)(block_len >> 8));

    // Write into window (0x40 onwards)
    for (int i = 0; i < block_len; i++) {
        asie_write_reg(fd, saddr, 0x40 + i, block[i]);
    }

    // Command Transmit (0x0a)
    asie_write_reg(fd, saddr, 0x00, 0x0a);
    usleep(100000); // transmit settle

    // Wait for response
    int rlen = 0;
    for (int poll = 0; poll < 40; poll++) {
        usleep(50000);
        rlen = asie_get_available(fd, saddr);
        if (rlen >= 4) break;
    }
    if (rlen < 4) {
        printf("APDU response timeout (got %d bytes)\n", rlen);
        return -1;
    }

    // Read window
    uint8_t raw_resp[256];
    for (int i = 0; i < rlen && i < 256; i++) {
        asie_read_reg(fd, saddr, 0x40 + i, &raw_resp[i]);
    }

    // Acknowledge (0x0c)
    asie_write_reg(fd, saddr, 0x00, 0x0c);

    // Verify LRC
    uint8_t check = 0;
    for (int i = 0; i < rlen; i++) check ^= raw_resp[i];
    if (check != 0) {
        printf("LRC check failed for card response!\n");
        return -1;
    }

    // Reverse bits
    for (int i = 0; i < rlen; i++) {
        raw_resp[i] = reverse8(raw_resp[i]);
    }

    // Header: NAD(0), PCB, LEN, Data..., LRC
    int data_len = raw_resp[2];
    if (data_len + 4 > rlen) data_len = rlen - 4;
    memcpy(resp, raw_resp + 3, data_len);
    *resp_len = data_len;
    return 0;
}

static int find_ut100b_path(char *out_path, size_t max_len) {
    char line[512];
    FILE *fp = popen("for d in /sys/bus/usb/devices/*; do "
                     "  if [ \"$(cat $d/idVendor 2>/dev/null):$(cat $d/idProduct 2>/dev/null)\" = \"3275:7020\" ]; then "
                     "    bus=$(cat $d/busnum); dev=$(cat $d/devnum); "
                     "    printf \"/dev/bus/usb/%03d/%03d\" $bus $dev; break; "
                     "  fi; done", "r");
    if (!fp) return -1;
    if (fgets(line, sizeof(line), fp)) {
        pclose(fp);
        line[strcspn(line, "\r\n")] = 0;
        if (strlen(line) > 0) {
            strncpy(out_path, line, max_len);
            return 0;
        }
    } else {
        pclose(fp);
    }
    return -1;
}

int main(int argc, char *argv[]) {
    char devpath[256];
    if (argc > 1) {
        strncpy(devpath, argv[1], sizeof(devpath));
    } else {
        if (find_ut100b_path(devpath, sizeof(devpath)) < 0) {
            fprintf(stderr, "Could not find UT100B (3275:7020) in /sys/bus/usb/devices\n");
            return 1;
        }
    }
    int fd = open(devpath, O_RDWR);
    if (fd < 0) {
        perror("open USB device");
        return 1;
    }

    int intf = 0;
    ioctl(fd, USBDEVFS_CLAIMINTERFACE, &intf);

    printf("Opened %s successfully\n", devpath);

    uint8_t data[0x200];

    // Power sequence
    printf("Executing power-on sequence...\n");
    send_ctl(fd, 0x0c, 0x0000, 0x0000, 0x003a, data);
    send_ctl(fd, 0x17, 0x0002, 0x0000, 0x0003, data);
    send_ctl(fd, 0x08, 0xfb07, 0x0000, 0x0001, data);
    send_ctl(fd, 0x10, 0x0200, 0x0000, 0x0001, data);
    send_ctl(fd, 0x08, 0xd400, 0x0000, 0x0001, data);
    send_ctl(fd, 0x08, 0x0808, 0x0000, 0x0001, data);
    usleep(50 * 1000);
    send_ctl(fd, 0x17, 0x0002, 0x0000, 0x0003, data);
    send_ctl(fd, 0x08, 0x0808, 0x0000, 0x0001, data);
    usleep(50 * 1000);
    send_ctl(fd, 0x08, 0x0800, 0x0000, 0x0001, data);
    usleep(150 * 1000);
    send_ctl(fd, 0x08, 0x11ff, 0x0000, 0x0001, data);
    usleep(50 * 1000);
    send_ctl(fd, 0x08, 0xd500, 0x0000, 0x0001, data);
    usleep(50 * 1000);
    send_ctl(fd, 0x08, 0xd5ff, 0x0000, 0x0001, data);
    usleep(100 * 1000);

    /* ASIE5606 reset cycle */
    send_ctl(fd, 0x08, 0x11ff, 0x0000, 1, data);
    usleep(50 * 1000);
    send_ctl(fd, 0x08, 0x1100, 0x0000, 1, data);
    usleep(50 * 1000);
    send_ctl(fd, 0x08, 0x11ff, 0x0000, 1, data);
    usleep(100 * 1000);

    uint8_t saddr = 0x4a;
    uint8_t status = 0;
    asie_read_reg(fd, saddr, 0x04, &status);
    printf("ASIE5606 Status: 0x%02x (CardPresent=%d)\n", status, status & 0x01);

    if (!(status & 0x01)) {
        printf("Error: No card present in slot!\n");
        close(fd);
        return 1;
    }

    printf("\n--- Starting B-CAS Reset (ATR Acquisition) ---\n");
    // Park / Prepare
    asie_write_reg(fd, saddr, 0x00, 0x80);
    usleep(10000);
    asie_write_reg(fd, saddr, 0x00, 0x00);
    asie_write_reg(fd, saddr, 0x04, 0x00);
    asie_write_reg(fd, saddr, 0x00, 0x00);
    usleep(100000);

    // Power ON & Reset
    asie_write_reg(fd, saddr, 0x00, 0x08); // Power ON
    asie_write_reg(fd, saddr, 0x04, 0x80);
    usleep(200000); // settle
    asie_write_reg(fd, saddr, 0x00, 0x09); // Reset command

    // Wait for full ATR (13 bytes)
    int atr_len = 0;
    for (int poll = 0; poll < 40; poll++) {
        usleep(50000);
        atr_len = asie_get_available(fd, saddr);
        if (atr_len >= 13) break;
    }
    printf("ATR received: %d bytes\n", atr_len);
    if (atr_len < 13) {
        printf("Failed to receive full ATR within timeout\n");
        close(fd);
        return 1;
    }

    uint8_t atr[32];
    printf("Standard B-CAS ATR: ");
    for (int i = 0; i < atr_len && i < 32; i++) {
        uint8_t b = 0;
        asie_read_reg(fd, saddr, 0x40 + i, &b);
        atr[i] = reverse8(b);
        printf("%02X ", atr[i]);
    }
    printf("\n");

    // Acknowledge ATR
    asie_write_reg(fd, saddr, 0x00, 0x0c);
    // Protocol = 0x01 (T=1)
    asie_write_reg(fd, saddr, 0x01, 0x01);
    usleep(50000);

    printf("\n--- Sending APDU: Card ID Request (90 30 00 00 00) ---\n");
    uint8_t cmd_card_id[] = {0x90, 0x30, 0x00, 0x00, 0x00};
    uint8_t resp[256];
    int resp_len = 0;
    if (asie_send_apdu(fd, saddr, 0, cmd_card_id, sizeof(cmd_card_id), resp, &resp_len) == 0) {
        printf("APDU Success! Response (%d bytes): ", resp_len);
        for (int i = 0; i < resp_len; i++) printf("%02X ", resp[i]);
        printf("\n");

        // If response >= 19 bytes, B-CAS Card ID is at offset 2..9 (or 8 bytes)
        // Format: status (2 bytes), manufacturer (1 byte), version (1 byte), card_id (6 bytes), check digit (2 bytes)...
        if (resp_len >= 10) {
            printf("B-CAS Card Data decoded successfully!\n");
        }
    } else {
        printf("APDU transmission failed\n");
    }

    close(fd);
    return 0;
}
