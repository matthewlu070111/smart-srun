include $(TOPDIR)/rules.mk

PKG_NAME:=luci-app-jxnu-srun
PKG_VERSION:=1.0.1-0.6
PKG_RELEASE:=1

include $(INCLUDE_DIR)/package.mk

LUCI_TITLE:=JXNU Campus Network
LUCI_DEPENDS:=+python3-light
LUCI_PKGARCH:=all
LUCI_DESCRIPTION:=LuCI app for JXNU SRun: auto login, night hotspot switching, backoff retry, and developer switch testing.

define Package/$(PKG_NAME)/postinst
#!/bin/sh
[ -n "$$IPKG_INSTROOT" ] || {
	chmod 0755 /etc/init.d/jxnu_srun 2>/dev/null
	chmod 0755 /usr/lib/jxnu_srun/client.py 2>/dev/null
}
exit 0
endef

include $(TOPDIR)/feeds/luci/luci.mk

# call BuildPackage - OpenWrt buildroot signature
