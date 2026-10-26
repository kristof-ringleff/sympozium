/**
 * The command the console hands an operator when the namespace has no Celln
 * fleet. Celln is part of the default install and holds no model, so there is
 * no provider, key or backend to choose: an Agent brings its own connection
 * later. The wording of the privilege notice follows fleetPrivilegeNotice in
 * cmd/sympozium/celln_fleet_install.go.
 */

/** The install command; `-n` scopes the control plane's namespace defaults, nothing about the fleet. */
export const FLEET_INSTALL_COMMAND = "sympozium install";

/** What the install deploys on the nodes, as the CLI prints it before doing so. */
export const FLEET_PRIVILEGE_NOTICE = {
  nodeLabel: "celln.dev/kvm=true",
  daemonSets: ["celln-node", "celln-node-configure"],
  mounts: ["/dev/kvm", "/boot", "/lib/modules", "/var/lib/sympozium-celln"],
  optOut: "--no-celln",
} as const;

export function fleetInstallCommand(): string {
  return FLEET_INSTALL_COMMAND;
}
