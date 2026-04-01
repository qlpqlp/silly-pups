{ pkgs ? import <nixpkgs> {} }:
let
  shibe-shell = pkgs.writeShellScriptBin "run.sh" ''
    echo "🐕 Shibe Shell starting..."

    # Bind to DogeBOX-injected IP and port 10000
    BIND_IP="''${DBX_PUP_IP:-127.0.0.1}"

    # Set up SSH directory with proper permissions
    mkdir -p /storage/.ssh
    chmod 700 /storage/.ssh

    # If SSH private key is provided via configuration, add it
    if [ -n "''${DBX_CONFIG_SSH_PRIVATE_KEY}" ]; then
      echo "📝 Setting up SSH private key from configuration..."
      echo "''${DBX_CONFIG_SSH_PRIVATE_KEY}" > /storage/.ssh/dogebox_key
      chmod 600 /storage/.ssh/dogebox_key
      echo "✅ SSH key ready at /storage/.ssh/dogebox_key"
    fi

    # Create .bashrc with help and easter egg functions
    cat > /storage/.bashrc << 'BASHRC_EOF'
# Shibe Shell Help & Easter Eggs 🐕

help() {
  cat << 'HELP_EOF'

📚 SHIBE SHELL - Setup Instructions

🔐 SSH Setup:
  1. Enable SSH on DogeBOX in Settings → Remote Access
  2. Generate SSH Key pair and add public key to Settings → Remote Access → + Add Key
  3. Copy your PRIVATE KEY and run these commands:

     mkdir -p /storage/.ssh
     chmod 700 /storage/.ssh
     echo 'PASTE_YOUR_PRIVATE_KEY_HERE' > /storage/.ssh/dogebox_key
     chmod 600 /storage/.ssh/dogebox_key

  4. Connect to your DogeBOX:
     ssh -i /storage/.ssh/dogebox_key shibe@dogebox

💡 Tips:
  - This web terminal is sandboxed in a container
  - Use SSH for full host access
  - Type 'help' anytime to see this message
  - Try 'moon' or 'doge' for easter eggs!

HELP_EOF
}

moon() {
  cat << 'MOON_EOF'
                   ___---___
                .--         --.
              ./   ()      .-. \.
             /   o    .   (D  )  \
            / .            '-'    \
           | ()    .  O         .  |
          |                         |
          |    o           ()       |
          |       .--.          O   |
           | .   | D  |            |
            \    `.__.'    o   .  /
             \                   /
              `\  o    ()      /'
                `--___   ___--'
                      ---

    🌙 Much Moon, Such Night 🌙
MOON_EOF
}

doge_print() {
  cat << 'DOGE_EOF'

                 ;i.
                  MYL                    .;i.
                  MYY;                .;iii;;.
                 ;$YY$i._           .iiii;;;;;
                .iiiYYYYYYiiiii;;;;i;iii;; ;;;
              .;iYYYYYYiiiiiiYYYiiiiiii;;  ;;;
           .YYYY$$$$YYYYYYYYYYYYYYYYiii;; ;;;;
         .YYY$$$$$$YYYYYY$$$$iiiY$$$$$$$ii;;;;
        :YYYY`,  YYYYYY$$$$$YYYYYYYi$$$$$iiiii;
        YYYY: \  :YYYY$$P"````"YYYYMMMMMMMMiiYY.
     `.;$$M$$b.,dYY$$Yi; .(     .YYMMM$$$MMMMYY
   .._$MMMMM$!YYYYYYYYYi;.`"  .;iiMMM$MMMMMMMYY
    ._$MMMP` ```""4$$$$$iiiiiiii$MMMMMMMMMMMMMY;
     MMMM$:       :$$$$$$$MMMMMMMMMMM$$MMMMMMMYYL
    :MMMM$$.    .;PPDOGEOINMMMMMMM$$$$MMMMMMiYYU:
     iMM$$;;: ;;;;i$$$$$$$MMMMM$$$$MMMMMMMMMMYYYYY
     `$$$$i .. ``:iiii!*"``.$$$$$$$$$MMMMMMM$YiYYY
      :Y$$iii;;;.. ` ..;;i$$$$$$$$$MMMMMM$$YYYYiYY:
       :$$$$$iiiiiii$$$$$$$$$$$MMMMMMMMMMYYYYiiYYYY.
        `$$$$$$$$$$$$$$$$$$$$MMMMMMMM$YYYYYiiiYYYYYY
         YY$$$$$$$$$$$$$$$$MMMMMMM$$YYYiiiiiiYYYYYYY
        :YYYYYY$$$$$$$$$$$$$$$$$$YYYYYYYiiiiYYYYYYi'

  Much Coin, Such Currency, Wow 🐕
DOGE_EOF
}

alias help="help"
alias moon="moon"
alias doge="doge_print"
alias Doge="doge_print"
alias Dogecoin="doge_print"
BASHRC_EOF

    # Create shell wrapper that sources .bashrc
    cat > /storage/shell-wrapper.sh << 'WRAPPER_EOF'
#!/${pkgs.bash}/bin/bash
source /storage/.bashrc
exec ${pkgs.bash}/bin/bash -i
WRAPPER_EOF
    chmod +x /storage/shell-wrapper.sh

    # Run ttyd with the wrapper script
    export HOME=/storage
    exec ${pkgs.ttyd}/bin/ttyd \
      -p 10000 \
      -i "''${BIND_IP}" \
      -T xterm-256color \
      --writable \
      --client-option fontSize=16 \
      --client-option fontFamily="monospace" \
      --client-option disableLeaveAlert=true \
      --client-option cursorBlink=true \
      /storage/shell-wrapper.sh
  '';
in
{ inherit shibe-shell; }
