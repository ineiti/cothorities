#!/usr/bin/env bash
set -e

VERSION=1.0-pre1

main(){
	if [ ! "$1" ]; then
		echo "Syntax is $0: (public|local)"
		exit 1
	fi
	if [ ! "$GOPATH" ]; then
		echo "'$GOPATH' not found"
		echo "Please install go: https://golang.org/doc/install"
		exit 1
	fi
	if ! echo $PATH | grep -q $GOPATH/bin; then
		echo "Please add '$GOPATH/bin' to your '$PATH'"
		PATH=$PATH:$GOPATH/bin
	fi
	case $( uname ) in
	Darwin)
		PATH_CO=~/Library/
		;;
	*)
		PATH_CO=~/.config/
		;;
	esac

	ACTION=$1
	shift
	case $ACTION in
	public)
		runPublic $@
		;;
	local)
		runLocal $@
		;;
	test)
		test $@
		;;
	esac
}


runLocal(){
	if [ ! "$1" ]; then
		echo "Please give the number of nodes to start"
		exit 1
	fi
	NBR=$1
	killall -9 conode || true
	go install github.com/dedis/cothority/conode

	for n in $( seq $NBR ); do
		co=co$n
		if ! grep -q Description $co/config.toml; then
			echo "Detected old files - deleting"
			rm -rf $co
		fi

		if [ ! -d $co ]; then
			echo -e "127.0.0.1:$((7000 + 2 * $n))\nConode $n\n$co" | conode setup
		fi
		conode -c $co/config.toml -d 3 &
	done

	grep -vh Description co*/group.toml > group.toml
}

runPublic(){
	# Get all arguments
	ARGS=""
	DEBUG=0
	while [ "$1" ]; do
		case $1 in
		"-update")
			UPDATE=yes
			;;
		"-update_rec")
			rm $2
			shift
			UPDATE_REC=yes
			;;
		"-debug")
			DEBUG=$2
			shift
			;;
		"-mail")
			MAIL=yes
			DEBUG=3
			;;
		*)
			ARGS="$ARGS $1"
			;;
		esac
		shift
	done
	migrate
	if [ "$UPDATE" ]; then
		update
	fi
	LOG=$( mktemp )
	if ! conode -d $DEBUG $@ | tee > $LOG; then
		if [ "$MAIL" ]; then
			mail conode-bugs@dedis.ch < $LOG
		fi
	fi
}

migrate(){
	PATH_VERSION=$PATH_CO/conode/version
	if [ -d $PATH_CO/cothorityd ]; then
		echo "Moving cothorityd-directory to conode"
		mv $PATH_CO/cothorityd $PATH_CO/conode
		echo 0.9 > $PATH_VERSION
	elif [ -d $PATH_CO/cothority ]; then
		echo "Moving cothority-directory to conode"
		mv $PATH_CO/cothority $PATH_CO/conode
		echo 0.9 > $PATH_VERSION
	fi
	PATH_CONODE=$PATH_CO/conode

	while [ "$( cat $PATH_VERSION )" != $VERSION ]; do
		case $( cat $PATH_VERSION ) in
		0.9)
			if [ -f $PATH_CONODE/config.toml ]; then
				echo "Renaming existing toml-files"
				mv $PATH_CONODE/config.toml $PATH_CONODE/private.toml
				mv $PATH_CONODE/group.toml $PATH_CONODE/public.toml
			fi
			echo 1.0 > $PATH_VERSION
			;;
		1.0)
			echo $VERSION > $PATH_VERSION
			;;
		esac
	done
}

update(){
	# As this script might also be updated, run the update in the /tmp-directory
	TMP=$( mktemp )
	TEST=$1
	cat - > $TMP << EOF
if [ ! "$TEST" ]; then
  go get -u github.com/dedis/cothority
  go get -u github.com/dedis/cothority/conode
fi
exec $GOPATH/src/github.com/dedis/cothority/run_conode.sh $ACTION -update_rec $TMP
EOF
	chmod a+x $TMP
	exec $TMP
}

test(){
	. $GOPATH/src/github.com/dedis/onet/app/libtest.sh

#	if [ "$1" != "-update_rec" ]; then
#		testUpdate
#	fi
	testMigrate
}

testUpdate(){
	update test
}
testMigrate(){
	testOK date
	PATH_CO=$( mktemp -d )
	for subdir in cothority cothorityd; do
		P=$PATH_CO/$subdir
		mkdir $P
		echo config > $P/config.toml
		echo group > $P/group.toml
		migrate
		testFileGrep group $PATH_CO/conode/public.toml
		testFileGrep config $PATH_CO/conode/private.toml
		testFileGrep $VERSION $PATH_CO/conode/version

		rm -rf $P/*
	done
}

main $@