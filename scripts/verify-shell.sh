#!/bin/sh
set -eu

for script in scripts/*.sh; do
	case "$(sed -n '1p' "$script")" in
		*"bash") bash -n "$script" ;;
		*) sh -n "$script" ;;
	esac
done
