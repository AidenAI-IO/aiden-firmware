# Keep OEM tools and libraries on the shell path without overriding login home.

OEM_HOME=/oem

case ":$PATH:" in
	*":$OEM_HOME:"*) ;;
	*) PATH="$PATH:$OEM_HOME:$OEM_HOME/bin:$OEM_HOME/usr/bin:$OEM_HOME/sbin:$OEM_HOME/usr/sbin" ;;
esac

case ":${LD_LIBRARY_PATH:-}:" in
	*":$OEM_HOME/usr/lib:"*) ;;
	*) LD_LIBRARY_PATH="$OEM_HOME/usr/lib:$OEM_HOME/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}" ;;
esac

export PATH LD_LIBRARY_PATH

unset OEM_HOME
