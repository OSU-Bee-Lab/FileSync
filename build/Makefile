# Cross-platform build pipeline for FileSync, using fyne-cross
# (https://github.com/fyne-io/fyne-cross). Run from the repo root as
# `make -f build/Makefile <target>`, or `cd build && make <target>`.
#
# Verified against fyne-cross v1.6.2: despite the `-local` flag's stated
# default of true on darwin, every target (including darwin, even when its
# arch matches the host) still routes through fyne-cross's container-engine
# abstraction, which requires a live Docker or Podman connection to
# orchestrate volumes/cleanup - `-local` only changes which image class is
# used within that flow, not whether Docker is needed at all. So: Docker
# (or Podman, or colima on macOS) must be running on the *build* machine for
# every target below. End users never see any of this - they just get a
# plain double-clickable binary/.app/.exe.
#
# Tooling this expects on the build machine (not on end-user machines):
#   go install github.com/fyne-io/fyne-cross@latest
#   Docker/Podman running (colima start, or Docker Desktop)

APP_ID := com.osubeelab.filesync
APP_NAME := FileSync
DIR := $(CURDIR)/..

.PHONY: dev build-darwin build-windows build-linux release clean

dev:
	go run $(DIR)

build-darwin:
	fyne-cross darwin -arch=amd64,arm64 -app-id $(APP_ID) -name $(APP_NAME) -dir $(DIR)
	# Ad-hoc codesign: doesn't make Gatekeeper trust the app (no paid Developer
	# ID here - see README), but avoids a class of spurious "app is damaged"
	# failures that can hit an entirely unsigned bundle.
	codesign --force --deep --sign - "$(DIR)/fyne-cross/dist/darwin-arm64/$(APP_NAME).app"
	codesign --force --deep --sign - "$(DIR)/fyne-cross/dist/darwin-amd64/$(APP_NAME).app"
	cd "$(DIR)/fyne-cross/dist/darwin-arm64" && zip -r -q "$(APP_NAME)-darwin-arm64.zip" "$(APP_NAME).app"
	cd "$(DIR)/fyne-cross/dist/darwin-amd64" && zip -r -q "$(APP_NAME)-darwin-amd64.zip" "$(APP_NAME).app"

build-windows:
	fyne-cross windows -arch=amd64 -app-id $(APP_ID) -name $(APP_NAME) -dir $(DIR)

build-linux:
	fyne-cross linux -arch=amd64 -app-id $(APP_ID) -name $(APP_NAME) -dir $(DIR)

# Collects every target's output into a flat release/vX.Y.Z/ folder for
# handing off (e.g. over Slack, per the lab's no-code-signing-budget
# distribution model - see README).
release: build-darwin build-windows build-linux
	mkdir -p "$(DIR)/release/$(VERSION)"
	cp "$(DIR)/fyne-cross/dist/darwin-arm64/$(APP_NAME)-darwin-arm64.zip" "$(DIR)/release/$(VERSION)/"
	cp "$(DIR)/fyne-cross/dist/darwin-amd64/$(APP_NAME)-darwin-amd64.zip" "$(DIR)/release/$(VERSION)/"
	cp "$(DIR)/fyne-cross/dist/windows-amd64/$(APP_NAME).exe" "$(DIR)/release/$(VERSION)/$(APP_NAME)-windows-amd64.exe"
	cp "$(DIR)/fyne-cross/dist/linux-amd64/$(APP_NAME).tar.xz" "$(DIR)/release/$(VERSION)/$(APP_NAME)-linux-amd64.tar.xz"
	@echo "Release artifacts in $(DIR)/release/$(VERSION)/"

clean:
	rm -rf "$(DIR)/fyne-cross"
