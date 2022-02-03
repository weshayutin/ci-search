# serve http://localhost/prowjobs.js a smaller version than http://prow.ci.openshift.org
# see the local prowjobs.js in this directory

# execute search w/
# ./search --path /var/tmp/oadp_ci_search --deck-uri=http://localhost  --interval 30m --v 7 --max-age 1h

sudo python -m  http.server 80

