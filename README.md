# ci-search

This repository helps developers identify frequently occuring CI failures by scraping JUnit results from Prow jobs stored in GCS to disk and then serving a web interface that can grep across those results. This allows quick triage of which issues are the most commonly occuring.

There are two primary components, the `search` binary which exposes the web interface and the `build-indexer` binary which extracts results from GCS. To build, run:

    make

To start the search process with defaults at http://localhost:8080 run:
    ./search --path /var/tmp/oadp_ci_search --deck-uri=https://prow.ci.openshift.org  --interval 15m --v 7

To start the search process at http://localhost:8080 run:

    ./search --path /directory/to/cache/results --config testgrid-like-config.yaml --interval 15m


`build-indexer` and either `grep` or `rg` (ripgrep) must be on the path.

The indexer runs at `--interval` and finds Prow job results that have finished since the last successful run completed. On startup the most recent 200 results are scraped. JUnit failure info is written to the `--path` directory as a `junit.failures` file that can be easily scanned. The modification date of the file is set to the finish timestamp of the build to assist in date searching.

The config file matches the testgrid config format and looks like:

```
test_groups:
- name: release-openshift-origin-installer-e2e-aws-4.0
  gcs_prefix: origin-ci-test/logs/release-openshift-origin-installer-e2e-aws-4.0
- name: release-openshift-origin-installer-e2e-aws-serial-4.0
  gcs_prefix: origin-ci-test/logs/release-openshift-origin-installer-e2e-aws-serial-4.0
...
- name: NAME
  gcs_prefix: <gcs_bucket_and_directory>
```

The search binary shells out to `rg` or `grep` (`rg` preferred for performance and better search options) and summarize the results it finds in modification order. Results are linked to the prow job result gubernator page.

## Deploying in OpenShift

* note the cluster must be setup with OCS with google storage backing.
e.g.
  * [google storage](https://console.cloud.google.com/storage/browser/origin-ci-test/logs/periodic-ci-openshift-oadp-operator-master-4.8-operator-e2e-aws-periodic-slack?pageState=(%22StorageObjectListTable%22:(%22f%22:%22%255B%255D%22))&prefix=&forceOnObjectsSortingFiltering=false)

  * [redhat-docs](https://access.redhat.com/documentation/en-us/red_hat_openshift_container_storage/4.5/html-single/deploying_and_managing_openshift_container_storage_using_google_cloud/index#creating-a-new-backing-store_gcp_gcp)

Do deploy ci-search in a new OpenShift project, you can use:

```console
$ oc new-project ci-search
$ oc new-app --name ci-search https://github.com/weshayutin/ci-search
$ oc create route edge --service=ci-search
$ oc get route -o jsonpath='{"https://"}{.status.ingress[0].host}{"/chart\n"}' ci-search
https://ci-search-ci-search.svc.ci.openshift.org/chart
```

Obviously you can use the other URI paths besides `/chart` as well.

## Future additions

* Improved visualization of results
* Include bugs and more search types like apiserver pod logs.
* Performance improvements
  * Allow user to opt in to context (allows rg to use mmap)
  * Filter the list of search results to an age (last 7 days) by default and pass in to rg
  * Allow rg to parallelize by doing sort at a higher level
  * Case sensitive search

## Performance constraints

Grep performance is directly proportional to the size and number of files in the directory to search. This means we want to minimize the number of files in the directory and their size to only the results that must be searched. Uncommon sources should be summarized or left out of the default search path (opt in vs out out).

There are a number of ways `rg` can be faster than grep that we aren't yet taking advantage of fully.
