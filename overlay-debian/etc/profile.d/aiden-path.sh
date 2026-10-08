# Make Aiden command-line tools available to root and ordinary login shells.
# Keep existing command precedence and avoid duplicates when sourced again.
case ":${PATH-}:" in
    *:/usr/lib/aiden:*) ;;
    *) PATH="${PATH:+${PATH}:}/usr/lib/aiden" ;;
esac
export PATH
